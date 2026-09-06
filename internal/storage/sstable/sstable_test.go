package sstable

import (
	"os"
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
		if err := w.Add([]byte(d.k), []byte(d.v)); err != nil {
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
