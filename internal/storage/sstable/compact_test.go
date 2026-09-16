package sstable

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"
)

// TestCompact_L0_MergeSortedNoDuplicates は、キー範囲が重複しない複数の L0 SSTable が
// マージ後に1つのソート済み SSTable として統合されることを確認する。
func TestCompact_L0_MergeSortedNoDuplicates(t *testing.T) {
	dataDir := t.TempDir()

	writeSSTable(t, dataDir, 0, "00001.sst", []struct {
		Key, Value string
		Act        ActType
	}{
		{"apple", "red", ActTypePut},
		{"banana", "yellow", ActTypePut},
	})
	writeSSTable(t, dataDir, 0, "00002.sst", []struct {
		Key, Value string
		Act        ActType
	}{
		{"cherry", "dark-red", ActTypePut},
		{"date", "brown", ActTypePut},
	})

	count := uint(3)
	if err := Compact(context.Background(), dataDir, &count, 0); err != nil {
		t.Fatalf("Compact() error: %v", err)
	}

	outPath := filepath.Join(dataDir, "00003.sst")
	got := readAllEntries(t, outPath)

	wantKeys := []string{"apple", "banana", "cherry", "date"}
	if len(got) != len(wantKeys) {
		t.Fatalf("got %d entries, want %d: %+v", len(got), len(wantKeys), got)
	}
	for i, k := range wantKeys {
		if string(got[i].Key) != k {
			t.Errorf("entry[%d].Key = %s, want %s", i, got[i].Key, k)
		}
	}
}

// TestCompact_L0_DuplicateKey_NewerFileWins は、同一キーが複数の L0 ファイルに存在する場合、
// より新しいファイル(ファイル名の連番が大きい方)の値が採用されることを確認する。
func TestCompact_L0_DuplicateKey_NewerFileWins(t *testing.T) {
	dataDir := t.TempDir()

	// 古いファイル
	writeSSTable(t, dataDir, 0, "00001.sst", []struct {
		Key, Value string
		Act        ActType
	}{
		{"apple", "old-value", ActTypePut},
	})
	// 新しいファイル
	writeSSTable(t, dataDir, 0, "00002.sst", []struct {
		Key, Value string
		Act        ActType
	}{
		{"apple", "new-value", ActTypePut},
	})

	count := uint(3)
	if err := Compact(context.Background(), dataDir, &count, 0); err != nil {
		t.Fatalf("Compact() error: %v", err)
	}

	outPath := filepath.Join(dataDir, "00003.sst")
	got := readAllEntries(t, outPath)

	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1: %+v", len(got), got)
	}
	if string(got[0].Value) != "new-value" {
		t.Errorf("Value = %s, want new-value (newer file should win)", got[0].Value)
	}
}

// TestCompact_L0_DeleteTombstonePreserved は、最新のエントリが削除マーカー(ActTypeDelete)の場合、
// マージ後も削除マーカーとして残ることを確認する(古い値へのフォールバックが起きないこと)。
func TestCompact_L0_DeleteTombstonePreserved(t *testing.T) {
	dataDir := t.TempDir()

	// 古いファイル: Put
	writeSSTable(t, dataDir, 0, "00001.sst", []struct {
		Key, Value string
		Act        ActType
	}{
		{"apple", "red", ActTypePut},
	})
	// 新しいファイル: Delete
	writeSSTable(t, dataDir, 0, "00002.sst", []struct {
		Key, Value string
		Act        ActType
	}{
		{"apple", "", ActTypeDelete},
	})

	count := uint(3)
	if err := Compact(context.Background(), dataDir, &count, 0); err != nil {
		t.Fatalf("Compact() error: %v", err)
	}

	outPath := filepath.Join(dataDir, "00003.sst")
	got := readAllEntries(t, outPath)

	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1: %+v", len(got), got)
	}
	if got[0].Act != ActTypeDelete {
		t.Errorf("Act = %v, want ActTypeDelete (tombstone must be preserved)", got[0].Act)
	}
}

// TestCompact_L0OverridesOverlappingL1 は、L0(新しいデータ)と L1(古いデータ)に同じキーが
// 存在する場合、L0 の値が優先されることを確認する。
func TestCompact_L0OverridesOverlappingL1(t *testing.T) {
	dataDir := t.TempDir()

	// L1: 古いデータ
	writeSSTable(t, dataDir, 1, "00001.sst", []struct {
		Key, Value string
		Act        ActType
	}{
		{"apple", "l1-old-value", ActTypePut},
	})
	// L0: 新しいデータ(同じキー)
	writeSSTable(t, dataDir, 0, "00002.sst", []struct {
		Key, Value string
		Act        ActType
	}{
		{"apple", "l0-new-value", ActTypePut},
	})

	count := uint(3)
	if err := Compact(context.Background(), dataDir, &count, 0); err != nil {
		t.Fatalf("Compact() error: %v", err)
	}

	outPath := filepath.Join(dataDir, "00003.sst")
	got := readAllEntries(t, outPath)

	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1: %+v", len(got), got)
	}
	if string(got[0].Value) != "l0-new-value" {
		t.Errorf("Value = %s, want l0-new-value (L0 should win over L1)", got[0].Value)
	}
}

// TestCompact_OnlyOverlappingNextLevelFilesIncluded は、L1 を対象に Compact する際、
// 次レベル(L2)のうちキー範囲が重複しないファイルはマージ対象に含まれないことを確認する。
func TestCompact_OnlyOverlappingNextLevelFilesIncluded(t *testing.T) {
	dataDir := t.TempDir()

	// L1: コンパクト対象
	writeSSTable(t, dataDir, 1, "00001.sst", []struct {
		Key, Value string
		Act        ActType
	}{
		{"banana", "l1-value", ActTypePut},
	})
	// L2: キー範囲が重複するファイル(取り込まれるべき)
	writeSSTable(t, dataDir, 2, "00002.sst", []struct {
		Key, Value string
		Act        ActType
	}{
		{"banana", "l2-overlap-value", ActTypePut},
	})
	// L2: キー範囲が重複しないファイル(取り込まれるべきではない)
	writeSSTable(t, dataDir, 2, "00003.sst", []struct {
		Key, Value string
		Act        ActType
	}{
		{"zebra", "l2-nonoverlap-value", ActTypePut},
	})

	count := uint(4)
	if err := Compact(context.Background(), dataDir, &count, 1); err != nil {
		t.Fatalf("Compact() error: %v", err)
	}

	outPath := filepath.Join(dataDir, "00004.sst")
	got := readAllEntries(t, outPath)

	wantKeys := []string{"banana"}
	if len(got) != len(wantKeys) {
		t.Fatalf("got %d entries, want %d: %+v (non-overlapping L2 file must be excluded)", len(got), len(wantKeys), got)
	}
	if string(got[0].Key) != "banana" {
		t.Errorf("Key = %s, want banana", got[0].Key)
	}
	if string(got[0].Value) != "l1-value" {
		t.Errorf("Value = %s, want l1-value (L1 should win over overlapping L2)", got[0].Value)
	}
}

// TestCompact_EmptyL0_NoOp は、L0 にファイルが存在しない場合でも Compact がエラーにならないことを確認する。
func TestCompact_EmptyL0_NoOp(t *testing.T) {
	dataDir := t.TempDir()

	count := uint(1)
	if err := Compact(context.Background(), dataDir, &count, 0); err != nil {
		t.Fatalf("Compact() with empty L0 should not error, got: %v", err)
	}
}

// writeSSTable は指定ディレクトリ(dir/l{level}/name)に SSTable を1つ書き込むテスト用ヘルパー。
// entries はソート済みであることが呼び出し側の責務。
func writeSSTable(t *testing.T, dataDir string, level uint, name string, entries []struct {
	Key, Value string
	Act        ActType
}) {
	t.Helper()

	levelDir := filepath.Join(dataDir, "l"+strconv.FormatUint(uint64(level), 10))
	w, err := NewWriter(levelDir, name)
	if err != nil {
		t.Fatalf("NewWriter(%s, %s) error: %v", levelDir, name, err)
	}
	for _, e := range entries {
		if err := w.Add([]byte(e.Key), []byte(e.Value), e.Act); err != nil {
			t.Fatalf("Writer.Add(%s) error: %v", e.Key, err)
		}
	}
	if err := w.Finish(); err != nil {
		t.Fatalf("Writer.Finish() error: %v", err)
	}
}

// readAllEntries は完成した SSTable ファイルを開いて全エントリを順番に読み出すテスト用ヘルパー。
func readAllEntries(t *testing.T, path string) []Entry {
	t.Helper()

	r, err := NewReader(path)
	if err != nil {
		t.Fatalf("NewReader(%s) error: %v", path, err)
	}
	defer r.Close()

	var got []Entry
	for r.Next() {
		e := r.Head()
		if e == nil {
			t.Fatalf("Head() returned nil while Next() reported true")
		}
		got = append(got, *e)
	}
	return got
}
