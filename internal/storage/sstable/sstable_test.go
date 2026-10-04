package sstable

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSSTable_Write(t *testing.T) {
	path := t.TempDir()

	w, err := NewWriter(path, "00001.sst")
	if err != nil {
		t.Fatal(err)
	}

	// LSM-tree の原則：ソート済みで Add する
	data := []struct{ k, v string }{
		{"apple", "red"},
		{"banana", "yellow"},
		{"cherry", "dark red"},
	}

	for _, d := range data {
		if err := w.Add([]byte(d.k), []byte(d.v), ActTypePut); err != nil {
			t.Fatal(err)
		}
	}

	if err := w.Finish(); err != nil {
		t.Fatal(err)
	}

	// ファイルが存在するか確認
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("SSTable created. Size: %d bytes", info.Size())
}

func TestWriter_MetaSizeIsFileSize(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWriter(dir, "00001.sst")
	if err != nil {
		t.Fatal(err)
	}
	w.blockSize = 1 // 複数ブロックにして、最終ブロックのサイズとの差を出す
	for _, k := range []string{"a", "b", "c"} {
		if err := w.Add([]byte(k), []byte("value-"+k), ActTypePut); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Finish(); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "00001.sst")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	if int64(r.meta.Size) != info.Size() {
		t.Errorf("meta.Size = %d, want file size %d", r.meta.Size, info.Size())
	}
}
