package manifest

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestManifest_ReopenRestoresFilesAndNextNum(t *testing.T) {
	path := filepath.Join(t.TempDir(), "MANIFEST")

	m, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := m.AddFile(m.NextFileNum()); err != nil {
			t.Fatal(err)
		}
	}
	m.Close()

	m2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer m2.Close()

	if got, want := m2.Files(), []int{3, 2, 1}; !slices.Equal(got, want) {
		t.Errorf("Files() = %v, want %v (newest first)", got, want)
	}
	if got := m2.NextFileNum(); got != 4 {
		t.Errorf("NextFileNum() = %d, want 4", got)
	}
}

// 書きかけ (改行なし) の末尾レコードは無視され、以降の追記を壊さない。
func TestManifest_TornTailIsDiscarded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "MANIFEST")
	if err := os.WriteFile(path, []byte("add 1\nadd 2\nadd"), 0644); err != nil {
		t.Fatal(err)
	}

	m, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := m.Files(), []int{2, 1}; !slices.Equal(got, want) {
		t.Errorf("Files() = %v, want %v", got, want)
	}
	if err := m.AddFile(3); err != nil {
		t.Fatal(err)
	}
	m.Close()

	m2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen after append to torn manifest: %v", err)
	}
	defer m2.Close()
	if got, want := m2.Files(), []int{3, 2, 1}; !slices.Equal(got, want) {
		t.Errorf("Files() = %v, want %v", got, want)
	}
}
