package memtable

import (
	"testing"
)

func TestMemtable_PutAndGet(t *testing.T) {
	m := NewMemtable()

	cases := []struct {
		key, val string
	}{
		{"apple", "red"},
		{"banana", "yellow"},
		{"grape", "purple"},
	}

	for _, c := range cases {
		m.Put([]byte(c.key), []byte(c.val))
	}

	for _, c := range cases {
		val, ok := m.Get([]byte(c.key))
		if !ok || string(val) != c.val {
			t.Errorf("expected %s for key %s, got %s", c.val, c.key, val)
		}
	}
}

func TestMemtable_SortedOrder(t *testing.T) {
	m := NewMemtable()
	// 順不同で挿入
	m.Put([]byte("z"), []byte("1"))
	m.Put([]byte("a"), []byte("2"))
	m.Put([]byte("m"), []byte("3"))

	// 内部をトラバースしてソートされているか確認
	var keys []string
	curr := m.head.next[0]
	for curr != nil {
		keys = append(keys, string(curr.key))
		curr = curr.next[0]
	}

	expected := []string{"a", "m", "z"}
	for i, k := range keys {
		if k != expected[i] {
			t.Errorf("expected %s at index %d, got %s", expected[i], i, k)
		}
	}
}

func TestMemtable_Keys(t *testing.T) {
	m := NewMemtable()
	m.Put([]byte("cat"), []byte("meow"))
	m.Put([]byte("dog"), []byte("bark"))
	m.Put([]byte("ant"), []byte("buzz"))

	keys := m.Keys()
	expected := []string{"ant", "cat", "dog"}

	for i, k := range keys {
		if string(k) != expected[i] {
			t.Errorf("expected %s at index %d, got %s", expected[i], i, k)
		}
	}
}
