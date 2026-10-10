package sstable

import (
	"distro-storage/internal/storage/action"
	"encoding/binary"
	"os"
	"path/filepath"
)

type Writer struct {
	file      *os.File
	level     uint
	index     []indexEntry
	offset    uint32
	blockSize uint32
	currSize  uint32
	minKey    []byte
	maxKey    []byte
	hasData   bool
}

// NewWriter は新しい SSTable ライターを作成します。
// 既存のファイルは上書きせず、存在する場合は os.ErrExist を返します。
func NewWriter(path, file string) (*Writer, error) {
	// path が存在しない場合は作成
	os.MkdirAll(path, 0755)
	f, err := os.OpenFile(filepath.Join(path, file), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0644)
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
func (w *Writer) Add(key, value []byte, act action.ActType) error {
	// minKey の追跡
	if !w.hasData {
		w.minKey = append([]byte(nil), key...)
		w.hasData = true
	}
	// ソート済みなので maxKey は常に更新
	w.maxKey = append([]byte(nil), key...)
	// log.Printf("Current Offset: %d, Current Block Size: %d", w.offset, w.currSize)
	// 新しいブロックの開始時にインデックスを記録
	if w.currSize == 0 || w.currSize > w.blockSize {
		w.index = append(w.index, indexEntry{
			key:    append([]byte(nil), key...),
			offset: w.offset,
		})
		w.currSize = 0
	}

	// データ書き込み [KeySize(4)|Key|ValSize(4)|Val|Act(1)]
	kvSize := 8 + len(key) + len(value) + 1
	buf := make([]byte, kvSize)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(len(key)))
	copy(buf[4:], key)
	binary.LittleEndian.PutUint32(buf[4+len(key):8+len(key)], uint32(len(value)))
	copy(buf[8+len(key):], value)
	buf[8+len(key)+len(value)] = byte(act)

	n, err := w.file.Write(buf)
	if err != nil {
		return err
	}

	w.offset += uint32(n)
	w.currSize += uint32(n)
	switch w.level {
	case 0:
		if w.currSize >= LEVEL_0_MAXFILESIZE {
			return FileSizeOverError
		}
	}
	return nil
}

// Finish はインデックスとフッターを書き込んでファイルを閉じます
func (w *Writer) Finish() error {
	indexOffset := w.offset
	// インデックスブロックの書き込み
	var indexBytesWritten uint32
	for _, entry := range w.index {
		binary.Write(w.file, binary.LittleEndian, uint32(len(entry.key)))
		w.file.Write(entry.key)
		binary.Write(w.file, binary.LittleEndian, entry.offset)
		// IndexBlock のサイズを追跡
		indexBytesWritten += 4 + uint32(len(entry.key)) + 4
	}

	// メタデータ (SSTableMeta) ブロックの書き込み
	metaOffset := indexOffset + indexBytesWritten
	// Size [Size(4)] ファイル全体のサイズ (データ + インデックス + メタデータ + フッター)
	metaBytes := 4 + 4 + uint32(len(w.minKey)) + 4 + uint32(len(w.maxKey))
	totalSize := metaOffset + metaBytes + footerSize
	binary.Write(w.file, binary.LittleEndian, totalSize)
	// MinKey [KeySize(4) | MinKey]
	binary.Write(w.file, binary.LittleEndian, uint32(len(w.minKey)))
	w.file.Write(w.minKey)
	// MaxKey [KeySize(4) | MaxKey]
	binary.Write(w.file, binary.LittleEndian, uint32(len(w.maxKey)))
	w.file.Write(w.maxKey)

	// フッターの書き込み (インデックスとメタデータの開始位置を記録)
	footer := make([]byte, 12)
	binary.LittleEndian.PutUint32(footer[0:4], indexOffset)
	binary.LittleEndian.PutUint32(footer[4:8], metaOffset) // MetaData Offset
	binary.LittleEndian.PutUint32(footer[8:12], 0xABCD)    // Magic Number
	w.file.Write(footer)

	return w.file.Close()
}
