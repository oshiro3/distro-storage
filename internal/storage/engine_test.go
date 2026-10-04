package storage

import (
	"testing"
)

func TestEngine_RestartRecovery(t *testing.T) {
	tmpDir := t.TempDir()

	// 1. エンジンを起動して書き込む
	e1, _ := NewEngine(tmpDir)
	e1.Put([]byte("hero"), []byte("Skywalker"))
	e1.Put([]byte("villain"), []byte("Vader"))
	// WAL に保存されているが、この後 e1 を「クラッシュ」させると仮定

	// 2. 新しいエンジンインスタンスを同じディレクトリで起動（リカバリ）
	e2, err := NewEngine(tmpDir)
	if err != nil {
		t.Fatalf("Recovery failed: %v", err)
	}

	// 3. データが復元されているか確認
	val, find, err := e2.Get([]byte("hero"))
	if err != nil {
		t.Fatalf("Get(hero) error: %v", err)
	}
	if !find || string(val) != "Skywalker" {
		t.Errorf("Expected Skywalker, got %s", val)
	}

	val2, _, err := e2.Get([]byte("villain"))
	if err != nil {
		t.Fatalf("Get(villain) error: %v", err)
	}
	if string(val2) != "Vader" {
		t.Errorf("Expected Vader, got %s", val2)
	}
}
