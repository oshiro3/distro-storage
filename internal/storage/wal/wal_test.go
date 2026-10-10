package wal

import (
	"distro-storage/internal/storage/action"
	"path/filepath"
	"testing"
)

func TestWAL_WriteAndReplay(t *testing.T) {
	// 1. テスト用のテンポラリディレクトリ作成
	tmpDir := t.TempDir()
	walPath := filepath.Join(tmpDir, "test.wal")

	// 2. WALの作成と書き込み
	w, err := NewLogWriter(walPath)
	if err != nil {
		t.Fatalf("failed to create WAL: %v", err)
	}

	testData := []struct {
		t   action.ActType
		key string
		val string
	}{
		{action.ActTypePut, "user:1", "alice"},
		{action.ActTypePut, "user:2", "bob"},
		{action.ActTypeDelete, "user:1", ""},
	}

	for _, d := range testData {
		err := w.Write(d.t, []byte(d.key), []byte(d.val))
		if err != nil {
			t.Errorf("failed to write: %v", err)
		}
	}
	w.Close()

	// 3. 再起動のシミュレーション（新しく開き直す）
	w2, err := NewLogWriter(walPath)
	if err != nil {
		t.Fatalf("failed to reopen WAL: %v", err)
	}
	defer w2.Close()

	var replayedEntries []string
	err = w2.Replay(func(et action.ActType, key, value []byte) {
		replayedEntries = append(replayedEntries, string(key))
	})

	if err != nil {
		t.Fatalf("replay failed: %v", err)
	}

	// 4. 検証
	if len(replayedEntries) != len(testData) {
		t.Errorf("expected %d entries, got %d", len(testData), len(replayedEntries))
	}

	t.Logf("Replayed entries: %v", replayedEntries)
}
