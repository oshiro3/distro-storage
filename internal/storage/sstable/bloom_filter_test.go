package sstable

import (
	"testing"
)

func TestBloomFilter(t *testing.T) {
	n := 1000
	bf := NewBloomFilter(n, 10)

	// 1. データの追加
	keys := [][]byte{
		[]byte("apple"),
		[]byte("banana"),
		[]byte("cherry"),
	}
	for _, k := range keys {
		bf.Add(k)
	}

	// 2. 確実に存在するはずのデータの確認 (False Negative がないこと)
	for _, k := range keys {
		if !bf.MayContain(k) {
			t.Errorf("BloomFilter should contain %s", k)
		}
	}

	// 3. 存在しないデータの確認 (ほとんどの場合 false になること)
	if bf.MayContain([]byte("durian")) {
		// 1%程度の確率でここに来る可能性があるが、この少数データではほぼ起きない
		t.Log("Note: False positive occurred (possible but rare)")
	} else {
		t.Log("Correctly filtered 'durian'")
	}
}
