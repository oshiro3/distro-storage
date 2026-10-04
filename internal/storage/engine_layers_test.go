package storage

import (
	"testing"

	"distro-storage/internal/storage/memtable"
)

// Engine.Get の層の優先順位 (activeMem > immutableMem > SSTable) を、
// Flush の非同期性に左右されず決定的に検証するための内部テスト。
// 公開 API だけでは「値が immutable にある」状態を固定できないため、このファイルだけは
// パッケージ内部から activeMem / immutableMem を直接操作する。
// 公開 API で確認できるものは internal/test/storage に置く。

func newLayerTestEngine(t *testing.T) *Engine {
	t.Helper()

	e, err := NewEngine(t.TempDir())
	if err != nil {
		t.Fatalf("NewEngine() error: %v", err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

// freezeActive は Flush を起動せずに activeMem を immutableMem へ切り替える。
func freezeActive(t *testing.T, e *Engine) {
	t.Helper()

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.immutableMem != nil {
		t.Fatal("immutableMem is already set")
	}
	e.immutableMem = e.activeMem
	e.activeMem = memtable.NewMemtable()
}

// immutable に値、active に tombstone がある場合は not found になる。
// tombstone を持たないキーは、従来どおり immutable から読める。
func TestEngine_Layers_ActiveTombstoneHidesImmutable(t *testing.T) {
	e := newLayerTestEngine(t)

	if err := e.Put([]byte("a"), []byte("1")); err != nil {
		t.Fatal(err)
	}
	if err := e.Put([]byte("b"), []byte("2")); err != nil {
		t.Fatal(err)
	}
	freezeActive(t, e)
	if err := e.Delete([]byte("a")); err != nil {
		t.Fatal(err)
	}

	if got, ok := e.Get([]byte("a")); ok {
		t.Errorf("Get(a) = %q, found = true, want not found (active tombstone must hide immutable)", got)
	}
	if got, ok := e.Get([]byte("b")); !ok || string(got) != "2" {
		t.Errorf("Get(b) = (%q, %v), want (2, true)", got, ok)
	}
}

// immutable に古い値があっても、active の新しい Put が勝つ (Delete → Put)。
func TestEngine_Layers_ActivePutAfterDeleteWinsOverImmutable(t *testing.T) {
	e := newLayerTestEngine(t)

	if err := e.Put([]byte("k"), []byte("v1")); err != nil {
		t.Fatal(err)
	}
	freezeActive(t, e)
	if err := e.Delete([]byte("k")); err != nil {
		t.Fatal(err)
	}
	if err := e.Put([]byte("k"), []byte("v2")); err != nil {
		t.Fatal(err)
	}

	if got, ok := e.Get([]byte("k")); !ok || string(got) != "v2" {
		t.Errorf("Get(k) = (%q, %v), want (v2, true)", got, ok)
	}
}

// immutable の tombstone が、SSTable の値を隠す。
// SSTable は Flush を同期実行して作る。
func TestEngine_Layers_ImmutableTombstoneHidesSST(t *testing.T) {
	e := newLayerTestEngine(t)

	if err := e.Put([]byte("a"), []byte("1")); err != nil {
		t.Fatal(err)
	}
	if err := e.Put([]byte("b"), []byte("2")); err != nil {
		t.Fatal(err)
	}
	freezeActive(t, e)
	e.flushImmutableMemtable() // "a", "b" を SST へ。immutableMem は nil に戻る

	if got, ok := e.Get([]byte("a")); !ok || string(got) != "1" {
		t.Fatalf("precondition: Get(a) = (%q, %v), want (1, true) from SSTable", got, ok)
	}

	if err := e.Delete([]byte("a")); err != nil {
		t.Fatal(err)
	}
	freezeActive(t, e) // tombstone を immutable へ (Flush はしない)

	if got, ok := e.Get([]byte("a")); ok {
		t.Errorf("Get(a) = %q, found = true, want not found (immutable tombstone must hide SST)", got)
	}
	if got, ok := e.Get([]byte("b")); !ok || string(got) != "2" {
		t.Errorf("Get(b) = (%q, %v), want (2, true)", got, ok)
	}
}
