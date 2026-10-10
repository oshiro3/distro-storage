package storage_test

import (
	"testing"

	"distro-storage/internal/storage/action"
)

// issue #8: ActType 定数の値を固定する。
//
// Act は WAL と SST のオンディスク形式 (1 バイト) に書かれる値なので、後から変えると互換性が壊れる。
// WAL / memtable / sstable は action.ActType を共通に使う (型変換をしない) ため、ここで値を固定すれば全体で固定される。
// 0 を未使用にしておくことで、ゼロ値や未初期化を Put と誤認しない。
func TestActType_ConstantValuesArePinned(t *testing.T) {
	cases := []struct {
		name string
		got  byte
		want byte
	}{
		{"action.ActTypePut", byte(action.ActTypePut), 1},
		{"action.ActTypeGet", byte(action.ActTypeGet), 2},
		{"action.ActTypeDelete", byte(action.ActTypeDelete), 3},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
}
