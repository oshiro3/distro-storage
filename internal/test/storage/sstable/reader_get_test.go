package sstable_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"distro-storage/internal/storage/sstable"
)

// layout は Writer の公開 API だけで SSTable のブロック構成を作り分けるための設定。
// Writer は累積サイズが 4096 を超えると次の Add で新しいブロックを切る。
// Put の値にパディングを足すことでブロック数を制御する。
// (L0 の FileSizeOverError は書き込み後に返るため、データ自体は書かれる)
type layout struct {
	name    string
	padding int
}

var layouts = []layout{
	{"single_block", 0},       // 全エントリが 1 ブロック
	{"multi_block", 2100},     // 約 2 エントリで 1 ブロック
	{"block_per_entry", 4100}, // 約 1 エントリで 1 ブロック
}

// buildSST は entries(ソート済み)から SSTable を書き出し、パスと
// 実際に書き込んだ(パディング済みの)エントリを返す。
func buildSST(t *testing.T, padding int, entries []sstable.Entry) (string, []sstable.Entry) {
	t.Helper()

	dir := t.TempDir()
	w, err := sstable.NewWriter(dir, "00001.sst")
	if err != nil {
		t.Fatalf("NewWriter() error: %v", err)
	}

	written := make([]sstable.Entry, 0, len(entries))
	for _, e := range entries {
		e := e
		if e.Act == sstable.ActTypePut {
			e.Value = append(append([]byte(nil), e.Value...), bytes.Repeat([]byte{'x'}, padding)...)
		}
		if err := w.Add(e.Key, e.Value, e.Act); err != nil && !errors.Is(err, sstable.FileSizeOverError) {
			t.Fatalf("Writer.Add(%q) error: %v", e.Key, err)
		}
		written = append(written, e)
	}
	if err := w.Finish(); err != nil {
		t.Fatalf("Writer.Finish() error: %v", err)
	}
	return filepath.Join(dir, "00001.sst"), written
}

func openReader(t *testing.T, path string) *sstable.Reader {
	t.Helper()

	r, err := sstable.NewReader(path)
	if err != nil {
		t.Fatalf("NewReader() error: %v", err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

func putEntry(key, value string) sstable.Entry {
	return sstable.Entry{Key: []byte(key), Value: []byte(value), Act: sstable.ActTypePut}
}

func deleteEntry(key string) sstable.Entry {
	return sstable.Entry{Key: []byte(key), Act: sstable.ActTypeDelete}
}

// gappedEntries は "b","d","f",...,"p" の 8 エントリ。キー間に必ず隙間がある。
func gappedEntries() []sstable.Entry {
	var entries []sstable.Entry
	for _, k := range []string{"b", "d", "f", "h", "j", "l", "n", "p"} {
		entries = append(entries, putEntry(k, "v-"+k))
	}
	return entries
}

// TestReader_Get_ExistingKeys は、全エントリ(先頭/中間/末尾)が正しい値で引けることを確認する。
func TestReader_Get_ExistingKeys(t *testing.T) {
	for _, l := range layouts {
		t.Run(l.name, func(t *testing.T) {
			path, entries := buildSST(t, l.padding, gappedEntries())
			r := openReader(t, path)

			for _, e := range entries {
				value, act, found, err := r.Get(e.Key)
				if err != nil {
					t.Fatalf("Get(%q) error: %v", e.Key, err)
				}
				if !found {
					t.Errorf("Get(%q): found = false, want true", e.Key)
					continue
				}
				if !bytes.Equal(value, e.Value) {
					t.Errorf("Get(%q): value = %.10q (len %d), want %.10q (len %d)", e.Key, value, len(value), e.Value, len(e.Value))
				}
				if act != sstable.ActTypePut {
					t.Errorf("Get(%q): act = %v, want ActTypePut", e.Key, act)
				}
			}
		})
	}
}

// TestReader_Get_MissingKeys は、存在しないキーが先頭エントリ等にフォールバックせず
// found=false, err=nil で返ることを確認する。
func TestReader_Get_MissingKeys(t *testing.T) {
	missing := []struct {
		name string
		key  string
	}{
		{"empty key", ""},
		{"below MinKey", "a"},
		{"gap between first and second", "c"},
		{"gap in the middle", "i"},
		{"gap between last two", "o"},
		{"above MaxKey", "q"},
		{"far above MaxKey", "zzz"},
		{"extends existing key", "bb"},
		{"existing key followed by NUL", "b\x00"},
	}

	for _, l := range layouts {
		t.Run(l.name, func(t *testing.T) {
			path, _ := buildSST(t, l.padding, gappedEntries())
			r := openReader(t, path)

			for _, c := range missing {
				t.Run(c.name, func(t *testing.T) {
					value, _, found, err := r.Get([]byte(c.key))
					if err != nil {
						t.Fatalf("Get(%q) error: %v", c.key, err)
					}
					if found {
						t.Errorf("Get(%q): found = true (value = %.10q), want false", c.key, value)
					}
				})
			}
		})
	}
}

// TestReader_Get_AcrossBlockBoundaries は、複数ブロックのファイルで
// ブロック境界をまたぐキーが正しく引けること、ブロック間の隙間のキーが
// 隣のブロックを読まずに not found になることを確認する。
func TestReader_Get_AcrossBlockBoundaries(t *testing.T) {
	// padding=2100 では [b,d] [f,h] [j,l] [n,p] の 4 ブロックになる
	path, entries := buildSST(t, 2100, gappedEntries())
	r := openReader(t, path)

	want := make(map[string][]byte, len(entries))
	for _, e := range entries {
		want[string(e.Key)] = e.Value
	}

	cases := []struct {
		key       string
		wantFound bool
	}{
		// 各ブロックの最後のキーと次ブロックの最初のキー
		{"d", true},
		{"f", true},
		{"l", true},
		{"n", true},
		// ブロック間の隙間(前ブロックの終端で打ち切られること)
		{"e", false},
		{"m", false},
	}
	for _, c := range cases {
		value, _, found, err := r.Get([]byte(c.key))
		if err != nil {
			t.Fatalf("Get(%q) error: %v", c.key, err)
		}
		if found != c.wantFound {
			t.Errorf("Get(%q): found = %v, want %v", c.key, found, c.wantFound)
			continue
		}
		if found && !bytes.Equal(value, want[c.key]) {
			t.Errorf("Get(%q): value = %.10q (len %d), want %.10q (len %d)", c.key, value, len(value), want[c.key], len(want[c.key]))
		}
	}
}

// TestReader_Get_Tombstone は、削除マーカーが ActTypeDelete として返り、
// 隣接するキーと混ざらないことを確認する。
func TestReader_Get_Tombstone(t *testing.T) {
	for _, l := range layouts {
		t.Run(l.name, func(t *testing.T) {
			path, entries := buildSST(t, l.padding, []sstable.Entry{
				putEntry("a", "1"),
				deleteEntry("b"),
				putEntry("c", "3"),
				deleteEntry("d"),
			})
			r := openReader(t, path)

			for _, e := range entries {
				value, act, found, err := r.Get(e.Key)
				if err != nil {
					t.Fatalf("Get(%q) error: %v", e.Key, err)
				}
				if !found {
					t.Errorf("Get(%q): found = false, want true", e.Key)
					continue
				}
				if act != e.Act {
					t.Errorf("Get(%q): act = %v, want %v", e.Key, act, e.Act)
				}
				if !bytes.Equal(value, e.Value) {
					t.Errorf("Get(%q): value = %.10q (len %d), want %.10q (len %d)", e.Key, value, len(value), e.Value, len(e.Value))
				}
			}

			// 削除マーカー同士・Put との間の隙間は not found
			for _, k := range []string{"", "aa", "bb", "cc", "e"} {
				_, _, found, err := r.Get([]byte(k))
				if err != nil {
					t.Fatalf("Get(%q) error: %v", k, err)
				}
				if found {
					t.Errorf("Get(%q): found = true, want false", k)
				}
			}
		})
	}
}

// TestReader_Get_EmptySSTable は、エントリが 1 件もない SSTable で
// メタ領域を誤読せず not found になることを確認する。
func TestReader_Get_EmptySSTable(t *testing.T) {
	path, _ := buildSST(t, 0, nil)
	r := openReader(t, path)

	for _, k := range []string{"", "x", "\x00", "zzz"} {
		value, _, found, err := r.Get([]byte(k))
		if err != nil {
			t.Fatalf("Get(%q) error: %v", k, err)
		}
		if found {
			t.Errorf("Get(%q): found = true (value = %q), want false", k, value)
		}
	}
}

// TestReader_Get_SingleEntry は、エントリが 1 件だけの SSTable の境界条件を確認する。
func TestReader_Get_SingleEntry(t *testing.T) {
	path, _ := buildSST(t, 0, []sstable.Entry{putEntry("m", "only")})
	r := openReader(t, path)

	value, _, found, err := r.Get([]byte("m"))
	if err != nil || !found || string(value) != "only" {
		t.Errorf("Get(m) = (%q, found=%v, err=%v), want (only, true, nil)", value, found, err)
	}
	for _, k := range []string{"", "a", "l", "n", "zzz"} {
		_, _, found, err := r.Get([]byte(k))
		if err != nil {
			t.Fatalf("Get(%q) error: %v", k, err)
		}
		if found {
			t.Errorf("Get(%q): found = true, want false", k)
		}
	}
}

// TestReader_Get_CorruptedFile は、破損ファイルに対して panic せず err を返すことを確認する。
func TestReader_Get_CorruptedFile(t *testing.T) {
	path, entries := buildSST(t, 0, []sstable.Entry{
		putEntry("apple", "red"),
		putEntry("banana", "yellow"),
		putEntry("cherry", "dark red"),
	})
	orig, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("truncated", func(t *testing.T) {
		lengths := []int{0, 5, 11, 12, len(orig) / 2, len(orig) - 13, len(orig) - 1}
		for _, n := range lengths {
			p := writeCorrupted(t, orig[:n])
			if !failsToReadAny(t, p, entries) {
				t.Errorf("truncated to %d bytes: expected an error from NewReader or Get, got none", n)
			}
		}
	})

	t.Run("bad data entry length", func(t *testing.T) {
		firstValLenOff := 4 + len(entries[0].Key)
		cases := []struct {
			name   string
			offset int
			value  uint32
		}{
			{"key length huge", 0, 0xFFFFFFFF},
			{"key length beyond data block", 0, uint32(len(orig)) + 100},
			{"value length huge", firstValLenOff, 0xFFFFFFFF},
			{"value length beyond data block", firstValLenOff, uint32(len(orig)) + 100},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				b := append([]byte(nil), orig...)
				binary.LittleEndian.PutUint32(b[c.offset:c.offset+4], c.value)
				r, err := sstable.NewReader(writeCorrupted(t, b))
				if err != nil {
					t.Fatalf("NewReader() error: %v (only the data block is corrupted)", err)
				}
				defer r.Close()

				// 破損エントリ自身と、それを読み飛ばす必要がある後続キーのどちらも err になる
				for _, e := range entries {
					if _, _, _, err := r.Get(e.Key); err == nil {
						t.Errorf("Get(%q): err = nil, want error", e.Key)
					}
				}
			})
		}
	})

	t.Run("bad footer", func(t *testing.T) {
		b := append([]byte(nil), orig...)
		binary.LittleEndian.PutUint32(b[len(b)-4:], 0xDEAD) // magic
		if _, err := sstable.NewReader(writeCorrupted(t, b)); err == nil {
			t.Error("NewReader() with bad magic: err = nil, want error")
		}

		b = append([]byte(nil), orig...)
		binary.LittleEndian.PutUint32(b[len(b)-12:], uint32(len(b))+1000) // indexOffset
		if r, err := sstable.NewReader(writeCorrupted(t, b)); err == nil {
			r.Close()
			t.Error("NewReader() with indexOffset beyond EOF: err = nil, want error")
		}
	})
}

func writeCorrupted(t *testing.T, b []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "corrupt.sst")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// failsToReadAny は NewReader か、いずれかのキーの Get が err を返せば true を返す。
func failsToReadAny(t *testing.T, path string, entries []sstable.Entry) bool {
	t.Helper()

	r, err := sstable.NewReader(path)
	if err != nil {
		return true
	}
	defer r.Close()
	for _, e := range entries {
		if _, _, _, err := r.Get(e.Key); err != nil {
			return true
		}
	}
	return false
}
