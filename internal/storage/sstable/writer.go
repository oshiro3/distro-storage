package sstable

import (
	"encoding/binary"
	"os"
	"path/filepath"
)

type Writer struct {
	file      *os.File
	index     []indexEntry
	offset    uint32
	blockSize uint32
	currSize  uint32
}

// NewWriter は新しい SSTable ライターを作成します
func NewWriter(path, file string) (*Writer, error) {
	// path が存在しない場合は作成
	os.MkdirAll(path, 0755)
	f, err := os.Create(filepath.Join(path, file))
	if err != nil {
		return nil, err
	}
	return &Writer{
		file:      f,
		blockSize: 4096, // 4KB ブロック
	}, nil
}

// Add はソート済みのキー・バリューを追加します
// (呼び出し側がソート済みであることを保証する必要があります)
func (w *Writer) Add(key, value []byte) error {
	// log.Printf("Current Offset: %d, Current Block Size: %d", w.offset, w.currSize)
	// 新しいブロックの開始時にインデックスを記録
	if w.currSize == 0 || w.currSize > w.blockSize {
		w.index = append(w.index, indexEntry{
			key:    append([]byte(nil), key...),
			offset: w.offset,
		})
		w.currSize = 0
	}

	// データ書き込み [KeySize(4)|Key|ValSize(4)|Val]
	kvSize := 8 + len(key) + len(value)
	buf := make([]byte, kvSize)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(len(key)))
	copy(buf[4:], key)
	binary.LittleEndian.PutUint32(buf[4+len(key):8+len(key)], uint32(len(value)))
	copy(buf[8+len(key):], value)

	n, err := w.file.Write(buf)
	if err != nil {
		return err
	}

	w.offset += uint32(n)
	w.currSize += uint32(n)
	return nil
}

// Finish はインデックスとフッターを書き込んでファイルを閉じます
func (w *Writer) Finish() error {
	indexOffset := w.offset
	// インデックスブロックの書き込み
	for _, entry := range w.index {
		binary.Write(w.file, binary.LittleEndian, uint32(len(entry.key)))
		w.file.Write(entry.key)
		binary.Write(w.file, binary.LittleEndian, entry.offset)
	}

	// フッターの書き込み (インデックスの開始位置を記録)
	footer := make([]byte, 8)
	binary.LittleEndian.PutUint32(footer[0:4], indexOffset)
	binary.LittleEndian.PutUint32(footer[4:8], 0xABCD) // Magic Number
	w.file.Write(footer)

	return w.file.Close()
}
