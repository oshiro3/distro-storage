package memtable

import (
	"bytes"
	"math/rand"
	"sync"
)

type ActType byte

const (
	maxLevel    = 12   // Skip List の最大高さ
	probability = 0.25 // 新しいレベルを追加する確率

	ActTypePut ActType = iota
	ActTypeDelete
)

// Node は Skip List のノードを表します
type Node struct {
	next []*Node // 各レベルの次のノードへのポインタ
	*Entry
}

// レコードの実体構造体
type Entry struct {
	Key   []byte
	Value []byte
	Act   ActType
}

// Memtable は Skip List を用いたメモリ内キー・バリューストアです
type Memtable struct {
	mu     sync.RWMutex
	head   *Node
	level  int
	length int
	size   uint32
}

func NewMemtable() *Memtable {
	return &Memtable{
		head:  &Node{next: make([]*Node, maxLevel)},
		level: 1,
		size:  0,
	}
}

// randomLevel は新しいノードの高さを確率的に決定する
func (m *Memtable) randomLevel() int {
	lvl := 1
	for rand.Float64() < probability && lvl < maxLevel {
		lvl++
	}
	return lvl
}

// Put はキーと値のペアを挿入または更新する
// SkipList への挿入はソート済みの状態を維持し O(log n) の時間で行われます
func (m *Memtable) Put(key, value []byte, act ActType) {
	m.mu.Lock()
	defer m.mu.Unlock()

	update := make([]*Node, maxLevel)
	curr := m.head

	// 1. 各レベルで挿入位置を探す
	for i := m.level - 1; i >= 0; i-- {
		for curr.next[i] != nil && bytes.Compare(curr.next[i].Key, key) < 0 {
			curr = curr.next[i]
		}
		update[i] = curr
	}

	curr = curr.next[0]

	// 2. キーが既に存在すれば値を更新（Update）
	if curr != nil && bytes.Equal(curr.Key, key) {
		// 古い値のサイズを引き、新しい値のサイズを足す
		m.size -= uint32(len(curr.Value))
		m.size += uint32(len(value))
		curr.Value = value
		curr.Act = act
		return
	}

	// 3. 新しいノードを挿入
	lvl := m.randomLevel()
	if lvl > m.level {
		for i := m.level; i < lvl; i++ {
			update[i] = m.head
		}
		m.level = lvl
	}

	newNode := &Node{
		next: make([]*Node, lvl),
		Entry: &Entry{
			Key:   key,
			Value: value,
			Act:   act,
		},
	}

	for i := range lvl {
		newNode.next[i] = update[i].next[i]
		update[i].next[i] = newNode
	}
	m.length++
	m.size += uint32(len(key) + len(value) + 8 + 8) // ポインタ分のオーバーヘッドを追加
}

// Get は指定されたキーに対応する値を返します
func (m *Memtable) Get(key []byte) ([]byte, ActType, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var current *Node = m.head
	for i := m.level - 1; i >= 0; i-- {
		for current.next[i] != nil && bytes.Compare(current.next[i].Key, key) < 0 {
			current = current.next[i]
		}
	}

	current = current.next[0]
	if current != nil && bytes.Equal(current.Key, key) {
		return current.Value, current.Act, true
	}
	return nil, 0, false
}

// Size は Memtable の現在のサイズをバイト単位で返します
func (m *Memtable) Size() uint32 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.size
}

// Keys は Memtable 内のすべてのキーをソート済みスライスとして返します
func (m *Memtable) Keys() [][]byte {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var keys [][]byte
	curr := m.head.next[0]
	for curr != nil {
		keys = append(keys, curr.Key)
		curr = curr.next[0]
	}
	return keys
}

// AllEntries は Memtable 内のすべてのエントリをキー・バリューのスライスとして返します
// key はソート済みの順序で返されます(SkipListは常にソート済みの状態を維持するため)
func (m *Memtable) AllEntries() []*Entry {
	m.mu.RLock()
	defer m.mu.RUnlock()
	res := make([]*Entry, 0, m.size)
	curr := m.head.next[0]
	for curr != nil {
		res = append(res, &Entry{curr.Key, curr.Value, curr.Act})
		curr = curr.next[0]
	}
	return res
}
