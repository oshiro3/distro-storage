package storage

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestEngine_AutoFlush(t *testing.T) {
	tmpDir := t.TempDir()
	t.Logf("Using temp dir: %s", tmpDir)
	e, _ := NewEngine(tmpDir)
	defer e.Close()

	// 閾値を超えるまで書き込む (4KB分)
	for i := range 100 {
		key := []byte{}
		key = fmt.Appendf(key, "key-%05d", i)

		val := []byte("some-large-value-to-fill-memtable-quickly")
		e.Put(key, val)
	}

	// Flush は非同期なので少し待つ
	time.Sleep(100 * time.Millisecond)

	// SSTファイルが生成されているか確認
	files, _ := filepath.Glob(filepath.Join(tmpDir, "l0/*.sst"))
	if len(files) == 0 {
		t.Error("Expected SSTable file to be created, but found none")
	} else {
		t.Logf("Successfully created %d SSTable files", len(files))
	}

	// Flush中/後でもデータが引けるか確認
	val, ok := e.Get([]byte("key-00000"))
	// for _, key := range e.immutableMem.Keys() {
	// 	// for _, key := range e.activeMem.Keys() {
	// 	t.Logf("Active Memtable Key: %s", key)
	// }
	if !ok {
		t.Error("Failed to get key-00000 after flush")
	} else {
		t.Logf("Retrieved value: %s...", val)
	}
}
