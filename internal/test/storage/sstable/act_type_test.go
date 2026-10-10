package sstable_test

import (
	"distro-storage/internal/storage/action"
	"os"
	"testing"

	"distro-storage/internal/storage/sstable"
)

// issue #8: SST の Act バイトの固定と、未知の Act の検出。

// actOffset は entries[i] の Act バイトの、ファイル内の位置を返す。
// エントリは [KeySize(4)|Key|ValSize(4)|Val|Act(1)] で、データブロックはファイルの先頭から始まる。
func actOffset(entries []sstable.Entry, i int) int {
	off := 0
	for _, e := range entries[:i] {
		off += 4 + len(e.Key) + 4 + len(e.Value) + 1
	}
	e := entries[i]
	return off + 4 + len(e.Key) + 4 + len(e.Value)
}

// Writer は Put/Delete をオンディスクに 1 / 3 として書く。この値は後から変えられない。
func TestSSTable_ActByteOnDisk(t *testing.T) {
	path, entries := buildSST(t, 0, []sstable.Entry{
		putEntry("a", "1"),
		deleteEntry("b"),
		putEntry("c", "3"),
	})
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	want := []byte{1, 3, 1}
	for i, e := range entries {
		if got := raw[actOffset(entries, i)]; got != want[i] {
			t.Errorf("Act byte of %q = %d, want %d", e.Key, got, want[i])
		}
	}
}

// 未知の Act を含むエントリは、Put/Delete として返さずエラーにする。
// 周囲の正常なエントリは、引き続き読める。
func TestReader_Get_UnknownActIsError(t *testing.T) {
	path, entries := buildSST(t, 0, []sstable.Entry{
		putEntry("a", "1"),
		putEntry("b", "2"),
		putEntry("c", "3"),
	})
	orig, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	corruptAt := actOffset(entries, 1) // "b"

	var notRejected, neighborsBroken []int
	for v := range 256 {
		act := byte(v)
		if act == byte(action.ActTypePut) || act == byte(action.ActTypeDelete) {
			continue
		}

		b := append([]byte(nil), orig...)
		b[corruptAt] = act
		r, err := sstable.NewReader(writeCorrupted(t, b))
		if err != nil {
			t.Fatalf("act=%d: NewReader() error: %v (only one Act byte is corrupted)", act, err)
		}

		if _, _, _, err := r.Get([]byte("b")); err == nil {
			notRejected = append(notRejected, v)
		}
		for _, k := range []string{"a", "c"} {
			if _, _, found, err := r.Get([]byte(k)); err != nil || !found {
				neighborsBroken = append(neighborsBroken, v)
				break
			}
		}
		r.Close()
	}

	if len(notRejected) > 0 {
		t.Errorf("Get(b) returned no error for %d unknown Act values: %v", len(notRejected), notRejected)
	}
	if len(neighborsBroken) > 0 {
		t.Errorf("entries next to a corrupted Act became unreadable for Act values: %v", neighborsBroken)
	}
}

// 走査 (Compact が使う Next/Head) でも、未知の Act のエントリを有効なエントリとして渡さない。
func TestReader_Iterate_UnknownActIsNotYielded(t *testing.T) {
	path, entries := buildSST(t, 0, []sstable.Entry{
		putEntry("a", "1"),
		putEntry("b", "2"),
	})
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	b[actOffset(entries, 1)] = 0xFF

	r, err := sstable.NewReader(writeCorrupted(t, b))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	for r.Next() {
		if act := r.Head().Act; act != action.ActTypePut && act != action.ActTypeDelete {
			t.Errorf("Next() yielded %q with unknown Act %d", r.Head().Key, act)
		}
	}
}
