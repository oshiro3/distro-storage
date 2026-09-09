package sstable

import "hash/fnv"

type BloomFilter struct {
	bitmap []byte
	m      uint32 // ビット数
	k      uint32 // ハッシュ関数の数
}

// NewBloomFilter は 1キーあたりのビット数(bitsPerKey)からフィルタを初期化する
func NewBloomFilter(n int, bitsPerKey int) *BloomFilter {
	max := uint32(n * bitsPerKey)
	if max < 64 {
		max = 64
	}

	// k = (m/n) * ln(2) ≒ bitsPerKey * 0.69
	k := uint32(float64(bitsPerKey) * 0.69)
	if k < 1 {
		k = 1
	}
	if k > 30 {
		k = 30
	}

	return &BloomFilter{
		bitmap: make([]byte, (max+7)/8),
		m:      max,
		k:      k,
	}
}

func (b *BloomFilter) Add(key []byte) {
	h1, h2 := b.hash(key)
	for i := uint32(0); i < b.k; i++ {
		// Gi(x) = h1(x) + i * h2(x)
		idx := (h1 + i*h2) % b.m
		b.bitmap[idx/8] |= (1 << (idx % 8))
	}
}

// MayContain はキーが存在する可能性があるかを判定する
func (b *BloomFilter) MayContain(key []byte) bool {
	if len(b.bitmap) == 0 {
		return true
	}
	h1, h2 := b.hash(key)
	for i := uint32(0); i < b.k; i++ {
		idx := (h1 + i*h2) % b.m
		if (b.bitmap[idx/8] & (1 << (idx % 8))) == 0 {
			return false // 絶対にない
		}
	}
	return true // あるかもしれない
}

// hash はダブルハッシュ技法を用いて k 個のハッシュ値を擬似的に生成する
func (b *BloomFilter) hash(data []byte) (uint32, uint32) {
	h := fnv.New32a()
	h.Write(data)
	h1 := h.Sum32()
	h2 := (h1 >> 17) | (h1 << 15) // シンプルな攪拌
	return h1, h2
}
