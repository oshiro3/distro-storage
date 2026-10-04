package storage_test

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"distro-storage/internal/storage"
)

// Engine.Get は、ある層で見つかったキーが削除マーカーなら、より古い層を見ずに not found を返す。
// 層の優先順位は activeMem > immutableMem > 新しい SSTable > 古い SSTable。
//
// Engine には公開の Flush が無いため、公開 API だけでテストする。
// 値を SSTable 側へ押し出すには flush() でパディングを書き込み、閾値超過による Flush を起こす。
// Flush は非同期なので、どの層にキーがあるかはテストから固定できない。
// そのため各テストは「どの層にあっても期待値が同じ」になる観点で書いている。
//
// SSTable 層を読むケース (flush() を使うテスト) は、Engine.Get が SST を開ける (#2) ことが前提。

type testEngine struct {
	*storage.Engine
	dir string
	n   int // flush() が書き込むパディングキーの連番
}

func newTestEngine(t *testing.T) *testEngine {
	t.Helper()

	dir := t.TempDir()
	e, err := storage.NewEngine(dir)
	if err != nil {
		t.Fatalf("NewEngine() error: %v", err)
	}
	t.Cleanup(func() { e.Close() })
	return &testEngine{Engine: e, dir: dir}
}

func (e *testEngine) put(t *testing.T, key, value string) {
	t.Helper()
	if err := e.Put([]byte(key), []byte(value)); err != nil {
		t.Fatalf("Put(%q) error: %v", key, err)
	}
}

func (e *testEngine) del(t *testing.T, key string) {
	t.Helper()
	if err := e.Delete([]byte(key)); err != nil {
		t.Fatalf("Delete(%q) error: %v", key, err)
	}
}

func (e *testEngine) assertFound(t *testing.T, key, want string) {
	t.Helper()

	got, ok, err := e.Get([]byte(key))
	if err != nil {
		t.Errorf("Get(%q) error: %v", key, err)
		return
	}
	if !ok {
		t.Errorf("Get(%q): found = false, want true (value %q)", key, want)
		return
	}
	if string(got) != want {
		t.Errorf("Get(%q) = %q, want %q", key, got, want)
	}
}

func (e *testEngine) assertNotFound(t *testing.T, key string) {
	t.Helper()

	got, ok, err := e.Get([]byte(key))
	if err != nil {
		t.Errorf("Get(%q) error: %v", key, err)
		return
	}
	if ok {
		t.Errorf("Get(%q) = %q, found = true, want not found (deleted key must not resurface)", key, got)
	}
}

func (e *testEngine) sstCount() int {
	files, _ := filepath.Glob(filepath.Join(e.dir, "l0", "*.sst"))
	return len(files)
}

// flush は、これまでの書き込みを SSTable へ押し出す。
// パディングを書き込んで閾値超過による Flush を起こし、新しい SST が生成されて
// ファイル数が落ち着くまで待つ。パディングのキーは "pad-" で始まり、テストのキーとは衝突しない。
func (e *testEngine) flush(t *testing.T) {
	t.Helper()

	before := e.sstCount()
	deadline := time.Now().Add(5 * time.Second)

	// Flush 中 (immutableMem が残っている間) は閾値を超えても新しい Flush は起きないので、
	// 新しい SST が現れるまで書き続ける
	for e.sstCount() == before {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for a flush to create an SSTable")
		}
		e.n++
		e.put(t, fmt.Sprintf("pad-%06d", e.n), "some-large-value-to-fill-memtable-quickly")
	}

	// 分割書き込みの途中でないこと (SST の数が増え終わっていること) を待つ
	stable := 0
	for last := e.sstCount(); stable < 3; {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the flush to finish")
		}
		time.Sleep(20 * time.Millisecond)
		if n := e.sstCount(); n != last {
			last, stable = n, 0
		} else {
			stable++
		}
	}
}

// --- Memtable 層だけで完結 (Flush を起こさない) ---

// Put → Delete → Get は not found になり、他のキーには影響しない。
func TestEngine_Delete_DeleteThenGet(t *testing.T) {
	e := newTestEngine(t)

	e.put(t, "a", "1")
	e.put(t, "b", "2")
	e.del(t, "a")

	e.assertNotFound(t, "a")
	e.assertFound(t, "b", "2")
}

// Delete → Put → Get は、新しい Put が勝って found になる。
func TestEngine_Delete_PutAfterDeleteWins(t *testing.T) {
	t.Run("Delete then Put", func(t *testing.T) {
		e := newTestEngine(t)

		e.del(t, "k")
		e.put(t, "k", "v")

		e.assertFound(t, "k", "v")
	})

	t.Run("Put, Delete, Put", func(t *testing.T) {
		e := newTestEngine(t)

		e.put(t, "k", "v1")
		e.del(t, "k")
		e.put(t, "k", "v2")

		e.assertFound(t, "k", "v2")
	})
}

// --- 古い値が下位層 (immutable / SSTable) にあるケース ---

// 古い値が active から押し出された後に Delete しても、Get は not found になる。
// 押し出された値は Flush の進行状況により immutable か SSTable のどちらかにある。
// どちらの層でも、active の tombstone が隠さなければならない。
func TestEngine_Delete_ActiveTombstoneHidesFlushedValue(t *testing.T) {
	e := newTestEngine(t)

	e.put(t, "a", "1")
	e.put(t, "b", "2")
	e.flush(t)

	e.del(t, "a")

	e.assertNotFound(t, "a")
	e.assertFound(t, "b", "2")
}

// Delete → Put → Get は、古い値が下位層にあっても新しい Put が勝つ。
func TestEngine_Delete_PutAfterDeleteWinsOverFlushedValue(t *testing.T) {
	e := newTestEngine(t)

	e.put(t, "k", "v1")
	e.flush(t)

	e.del(t, "k")
	e.put(t, "k", "v2")

	e.assertFound(t, "k", "v2")
}

// 新しい SSTable の tombstone が、古い SSTable の値を隠す。
func TestEngine_Delete_NewerSSTTombstoneHidesOlderSST(t *testing.T) {
	e := newTestEngine(t)

	e.put(t, "a", "1")
	e.put(t, "b", "2")
	e.flush(t) // "a" は古い SST へ

	e.del(t, "a")
	e.flush(t) // tombstone が新しい SST へ

	e.assertNotFound(t, "a")
	e.assertFound(t, "b", "2")
}
