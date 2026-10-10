package storage

import (
	"distro-storage/internal/storage/action"
	"path/filepath"
	"testing"

	"distro-storage/internal/storage/sstable"
)

// issue #8: WAL / memtable / sstable 間の Act の変換が、数値の偶然の一致に依存していないことを確認する。
// Replay 後の activeMem を直接見る必要があるため、パッケージ内部のテストにしている。

// WAL の Replay で復元した Memtable の Act は、Put のエントリなら ActTypePut になる。
func TestEngine_Replay_RestoredActIsPut(t *testing.T) {
	dir := t.TempDir()

	e, err := NewEngine(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"a", "b"} {
		if err := e.Put([]byte(k), []byte("v-"+k)); err != nil {
			t.Fatal(err)
		}
	}
	e.Close()

	e2, err := NewEngine(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e2.Close() })

	for _, k := range []string{"a", "b"} {
		_, act, ok := e2.activeMem.Get([]byte(k))
		if !ok {
			t.Errorf("replayed memtable has no %q", k)
			continue
		}
		if act != action.ActTypePut {
			t.Errorf("replayed Act of %q = %d, want ActTypePut (%d)", k, act, action.ActTypePut)
		}
	}
}

// l0Act は L0 の唯一の SST から key の Act を読む。
func l0Act(t *testing.T, e *Engine, key string) action.ActType {
	t.Helper()

	files, _ := filepath.Glob(filepath.Join(e.l0Dir(), "*.sst"))
	if len(files) != 1 {
		t.Fatalf("want 1 SSTable, got %d", len(files))
	}
	r, err := sstable.NewReader(files[0])
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	_, act, found, err := r.Get([]byte(key))
	if err != nil || !found {
		t.Fatalf("Get(%q) = (found=%v, err=%v), want (true, nil)", key, found, err)
	}
	return act
}

// Flush は memtable の Put/Delete を、SST の ActTypePut/ActTypeDelete として書く。
func TestEngine_Flush_WritesActTypes(t *testing.T) {
	e := newLayerTestEngine(t)

	if err := e.Put([]byte("a"), []byte("1")); err != nil {
		t.Fatal(err)
	}
	if err := e.Delete([]byte("b")); err != nil {
		t.Fatal(err)
	}
	freezeActive(t, e)
	e.flushImmutableMemtable()

	if got := l0Act(t, e, "a"); got != action.ActTypePut {
		t.Errorf("SST Act of a = %d, want ActTypePut (%d)", got, action.ActTypePut)
	}
	if got := l0Act(t, e, "b"); got != action.ActTypeDelete {
		t.Errorf("SST Act of b = %d, want ActTypeDelete (%d)", got, action.ActTypeDelete)
	}
}

// WAL から復元したデータを Flush しても、SST には ActTypePut として書かれる。
func TestEngine_ReplayThenFlush_WritesPutAct(t *testing.T) {
	dir := t.TempDir()

	e, err := NewEngine(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Put([]byte("a"), []byte("1")); err != nil {
		t.Fatal(err)
	}
	e.Close()

	e2, err := NewEngine(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e2.Close() })

	freezeActive(t, e2)
	e2.flushImmutableMemtable()

	if got := l0Act(t, e2, "a"); got != action.ActTypePut {
		t.Errorf("SST Act of replayed a = %d, want ActTypePut (%d)", got, action.ActTypePut)
	}
}
