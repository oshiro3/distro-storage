package storage_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"distro-storage/internal/storage"
)

// issue #4: 再起動で sstCount が 0 に戻り、既存 SST が見えなくなる / 上書きされる、の回帰テスト。
//
// WAL は Flush 後も切り詰められず、再起動時のリプレイで Flush 済みのキーも Memtable に戻ってしまう。
// そのままでは SST を読めなくても Get が成功してしまい、バグが隠れる。
// そこで再起動前に WAL を削除し、Flush 済みデータが SST にしか残っていない状態を作る
// (WAL が Flush 時に切り替わる本来の挙動と同じ状態)。

// restart は e を閉じ、WAL を捨てて、同じディレクトリでエンジンを開き直す。
func restart(t *testing.T, e *testEngine) *testEngine {
	t.Helper()

	if err := e.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
	wals, _ := filepath.Glob(filepath.Join(e.dir, "*.wal"))
	for _, p := range wals {
		if err := os.Remove(p); err != nil {
			t.Fatalf("remove WAL: %v", err)
		}
	}

	e2, err := storage.NewEngine(e.dir)
	if err != nil {
		t.Fatalf("NewEngine() after restart error: %v", err)
	}
	t.Cleanup(func() { e2.Close() })
	return &testEngine{Engine: e2, dir: e.dir}
}

// readL0 は l0/ 配下の全ファイルの内容をファイル名ごとに返す。
func readL0(t *testing.T, dir string) map[string][]byte {
	t.Helper()

	paths, err := filepath.Glob(filepath.Join(dir, "l0", "*.sst"))
	if err != nil {
		t.Fatalf("glob l0: %v", err)
	}
	files := make(map[string][]byte, len(paths))
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		files[filepath.Base(p)] = b
	}
	return files
}

func sameFiles(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for name, content := range a {
		if other, ok := b[name]; !ok || !bytes.Equal(content, other) {
			return false
		}
	}
	return true
}

// flushAfterRestart は再起動後のエンジンに書き込み続けて Flush を起こし、Flush が終わるまで待つ。
// 上書きバグがあると SST のファイル数は増えず内容だけが変わるため、
// ファイル数ではなく l0/ 全体の内容が before から変化したかで Flush の発生を判定する。
func (e *testEngine) flushAfterRestart(t *testing.T, before map[string][]byte) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)

	for sameFiles(before, readL0(t, e.dir)) {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for a flush after restart")
		}
		e.n++
		e.put(t, fmt.Sprintf("pad-%06d", e.n), "some-large-value-to-fill-memtable-quickly")
	}

	// Flush (分割書き込みを含む) が終わって l0/ の内容が落ち着くまで待つ
	stable := 0
	for last := readL0(t, e.dir); stable < 3; {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the flush to finish")
		}
		time.Sleep(20 * time.Millisecond)
		if cur := readL0(t, e.dir); !sameFiles(last, cur) {
			last, stable = cur, 0
		} else {
			stable++
		}
	}
}

// Flush → 再起動 → Get で、Flush 済みのキーが読める。
func TestEngine_Restart_FlushedKeysReadable(t *testing.T) {
	e := newTestEngine(t)

	e.put(t, "a", "1")
	e.put(t, "b", "2")
	e.flush(t)

	e2 := restart(t, e)

	e2.assertFound(t, "a", "1")
	e2.assertFound(t, "b", "2")
}

// 再起動前に複数の SST があっても、全てが読める (最大番号から再開するだけでなく、全 SST を発見する)。
func TestEngine_Restart_AllFlushedSSTsReadable(t *testing.T) {
	e := newTestEngine(t)

	e.put(t, "old", "1")
	e.flush(t)
	e.put(t, "new", "2")
	e.flush(t)
	if n := e.sstCount(); n < 2 {
		t.Fatalf("precondition: SST count = %d, want >= 2", n)
	}

	e2 := restart(t, e)

	e2.assertFound(t, "old", "1")
	e2.assertFound(t, "new", "2")
}

// 再起動前に Delete して Flush した tombstone も、再起動後に効く (削除済みキーが復活しない)。
func TestEngine_Restart_TombstoneSurvives(t *testing.T) {
	e := newTestEngine(t)

	e.put(t, "a", "1")
	e.put(t, "b", "2")
	e.flush(t)
	e.del(t, "a")
	e.flush(t)

	e2 := restart(t, e)

	e2.assertNotFound(t, "a")
	e2.assertFound(t, "b", "2")
}

// 再起動後の Flush は、既存の SST を上書き (truncate) せず、Flush 済みデータを失わない。
func TestEngine_Restart_FlushDoesNotOverwriteExistingSST(t *testing.T) {
	e := newTestEngine(t)

	e.put(t, "a", "1")
	e.put(t, "b", "2")
	e.flush(t)

	e2 := restart(t, e)
	before := readL0(t, e2.dir)
	if len(before) == 0 {
		t.Fatal("precondition: no SST before restart")
	}

	e2.put(t, "c", "3")
	e2.flushAfterRestart(t, before)

	// 既存の SST が 1 バイトも変わらず残っている
	after := readL0(t, e2.dir)
	for name, want := range before {
		got, ok := after[name]
		if !ok {
			t.Errorf("existing SST %s was removed by a flush after restart", name)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("existing SST %s was overwritten by a flush after restart (%d bytes -> %d bytes)", name, len(want), len(got))
		}
	}
	if len(after) <= len(before) {
		t.Errorf("SST count = %d, want > %d (the flush after restart must write a new file)", len(after), len(before))
	}

	// 再起動前後のデータが全て読める
	e2.assertFound(t, "a", "1")
	e2.assertFound(t, "b", "2")
	e2.assertFound(t, "c", "3")
}

// 存在しない dir でも起動できる。
func TestEngine_NewEngine_CreatesMissingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "not", "yet", "created")

	e, err := storage.NewEngine(dir)
	if err != nil {
		t.Fatalf("NewEngine(%q) error: %v, want the directory to be created", dir, err)
	}
	t.Cleanup(func() { e.Close() })

	te := &testEngine{Engine: e, dir: dir}
	te.put(t, "k", "v")
	te.assertFound(t, "k", "v")
}
