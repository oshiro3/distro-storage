package sstable

import "bytes"

// Iterator は SSTable から順番にデータを取得するためのインターフェース
type Iterator interface {
	Next() bool
	Head() *Entry
}

// Item は優先度付きキューに格納される要素
type Item struct {
	Iter Iterator
	// 小さいほど優先度が高い
	// 対象ファイルの作成順に等しい
	Priority int
}

type PriorityQueue []*Item

func (pq PriorityQueue) Len() int { return len(pq) }

func (pq PriorityQueue) Less(i, j int) bool {
	cmp := bytes.Compare(pq[i].Iter.Head().Key, pq[j].Iter.Head().Key)
	if cmp == 0 {
		// キーが同じ場合は Priority が小さい（新しい）方を先にPopさせる
		return pq[i].Priority > pq[j].Priority
	}
	return cmp < 0
}
func (pq PriorityQueue) Swap(i, j int) { pq[i], pq[j] = pq[j], pq[i] }

func (pq *PriorityQueue) Push(x any) { *pq = append(*pq, x.(*Item)) }

func (pq *PriorityQueue) Pop() any {
	old := *pq
	n := len(old)
	item := old[n-1]
	*pq = old[0 : n-1]
	return item
}
