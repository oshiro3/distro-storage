// Package manifest は、どの L0 SSTable が有効かを永続化する最小の Manifest を提供する
//
// 形式は追記型のテキストログで、1 行が 1 レコード ("add <ファイル番号>\n")
// 書き込みごとに fsync し、起動時に先頭から Replay して状態を復元する
package manifest

import (
	"bytes"
	"fmt"
	"os"
	"sync"
)

type Manifest struct {
	mu    sync.Mutex
	file  *os.File
	files []int // 追加順 (古い順)
	next  int   // 次に払い出すファイル番号
}

// Open は path の Manifest を開く (無ければ作る)
func Open(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}

	// 末尾の改行で終わっていない行は書きかけ (クラッシュ) として捨てる
	valid := 0
	if i := bytes.LastIndexByte(data, '\n'); i >= 0 {
		valid = i + 1
	}

	m := &Manifest{next: 1}
	for _, line := range bytes.Split(data[:valid], []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var n int
		if _, err := fmt.Sscanf(string(line), "add %d", &n); err != nil {
			return nil, fmt.Errorf("manifest %s: invalid record %q: %w", path, line, err)
		}
		m.files = append(m.files, n)
		m.ensureNextAbove(n)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0644)
	if err != nil {
		return nil, err
	}

	// 書きかけの末尾を落とさないと、次の追記がその行に連結されて壊れる
	if err := f.Truncate(int64(valid)); err != nil {
		f.Close()
		return nil, err
	}
	m.file = f
	return m, nil
}

// Files は有効な SSTable のファイル番号を新しい順で返す
func (m *Manifest) Files() []int {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]int, len(m.files))
	for i, n := range m.files {
		out[len(m.files)-1-i] = n
	}
	return out
}

// NextFileNum は未使用のファイル番号を返す
func (m *Manifest) NextFileNum() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	n := m.next
	m.next++
	return n
}

// AddFile は完成済みの SSTable n を有効として記録する (fsync 済みで戻る)
func (m *Manifest) AddFile(n int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, err := fmt.Fprintf(m.file, "add %d\n", n); err != nil {
		return err
	}
	if err := m.file.Sync(); err != nil {
		return err
	}
	m.files = append(m.files, n)
	m.ensureNextAbove(n)
	return nil
}

func (m *Manifest) Close() error {
	return m.file.Close()
}

func (m *Manifest) ensureNextAbove(n int) {
	if n >= m.next {
		m.next = n + 1
	}
}
