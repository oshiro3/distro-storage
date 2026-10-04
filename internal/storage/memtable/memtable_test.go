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
		m.Put([]byte(c.key), []byte(c.val), ActTypePut)
	}

	for _, c := range cases {
		val, act, ok := m.Get([]byte(c.key))
		if !ok || string(val) != c.val || act != ActTypePut {
			t.Errorf("expected key %s to have value %s: act is %d, got value %s: act is %d", c.key, c.val, ActTypePut, val, act)
		}
	}
}

func TestMemtable_DeleteAndNotGet(t *testing.T) {
	m := NewMemtable()

	cases := []struct {
		key, val string
	}{
		{"blad", "red"},
		{"bee", "yellow"},
		{"paper", "white"},
	}

	for _, c := range cases {
		m.Put([]byte(c.key), []byte(c.val), ActTypePut)
	}

	for _, c := range cases {
		m.Put([]byte(c.key), nil, ActTypeDelete)
	}

	for _, c := range cases {
		_, act, ok := m.Get([]byte(c.key))
		if !ok && act != ActTypeDelete {
			t.Errorf("expected key %s to be present, but it was not", c.key)
		}
	}
}

func TestMemtable_SortedOrder(t *testing.T) {
	m := NewMemtable()
	// 順不同で挿入
	m.Put([]byte("z"), []byte("1"), ActTypePut)
	m.Put([]byte("a"), []byte("2"), ActTypePut)
	m.Put([]byte("m"), []byte("3"), ActTypePut)

	// 内部をトラバースしてソートされているか確認
	var keys []string
	curr := m.head.next[0]
	for curr != nil {
		keys = append(keys, string(curr.Key))
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
	m.Put([]byte("cat"), []byte("meow"), ActTypePut)
	m.Put([]byte("dog"), []byte("bark"), ActTypePut)
	m.Put([]byte("ant"), []byte("buzz"), ActTypePut)

	keys := m.Keys()
	expected := []string{"ant", "cat", "dog"}

	for i, k := range keys {
		if string(k) != expected[i] {
			t.Errorf("expected %s at index %d, got %s", expected[i], i, k)
		}
	}
}
