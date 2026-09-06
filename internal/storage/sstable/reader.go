package sstable

import (
	"bytes"
	"encoding/binary"
	"io"
	"log"
	"os"
)

type Reader struct {
	file  *os.File
	index []indexEntry
}

// NewReader は SSTable リーダーを作成します
func NewReader(path string) (*Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	r := &Reader{file: f}
	if err := r.loadIndex(); err != nil {
		f.Close()
		return nil, err
	}
	return r, nil
}

// loadIndex は SSTable のインデックスを読み込む
func (r *Reader) loadIndex() error {
	stat, _ := r.file.Stat()
	size := stat.Size()

	// フッターの読み込み
	footer := make([]byte, 8)
	if _, err := r.file.ReadAt(footer, size-8); err != nil {
		return err
	}
	indexOffset := binary.LittleEndian.Uint32(footer[0:4])
	magic := binary.LittleEndian.Uint32(footer[4:8])
	if magic != 0xABCD {
		return os.ErrInvalid
	}

	// インデックスブロックの読み込み
	_, err := r.file.Seek(int64(indexOffset), io.SeekStart)
	if err != nil {
		return err
	}

	for {
		var keyLen uint32
		if err := binary.Read(r.file, binary.LittleEndian, &keyLen); err != nil || err == io.EOF {
			curr, _ := r.file.Seek(0, io.SeekCurrent)
			if curr >= size-8 {
				break
			}
			if err != io.EOF {
				return err
			}
			break
		}
		key := make([]byte, keyLen)
		r.file.Read(key)
		var offset uint32
		binary.Read(r.file, binary.LittleEndian, &offset)
		r.index = append(r.index, indexEntry{key: key, offset: offset})
	}
	return nil
}

// Get は指定されたキーに対応する値を返します
func (r *Reader) Get(key []byte) ([]byte, bool, error) {
	log.Printf("SSTable Get: key=%s", string(key))
	// バイナリサーチでインデックスを探索
	low, high := 0, len(r.index)-1
	var targetOffset uint32
	for low <= high {
		mid := (low + high) / 2
		cmp := bytes.Compare(r.index[mid].key, key)
		if cmp == 0 {
			targetOffset = r.index[mid].offset
			log.Printf("Found key at offset: %d\n", targetOffset)
			break
		} else if cmp < 0 {
			low = mid + 1
		} else {
			high = mid - 1
		}
	}

	// データブロックから値を読み込み
	if _, err := r.file.Seek(int64(targetOffset), io.SeekStart); err != nil {
		return nil, false, err
	}

	var keyLen uint32
	if err := binary.Read(r.file, binary.LittleEndian, &keyLen); err != nil {
		return nil, false, err
	}
	readKey := make([]byte, keyLen)
	r.file.Read(readKey)

	var valLen uint32
	if err := binary.Read(r.file, binary.LittleEndian, &valLen); err != nil {
		return nil, false, err
	}
	value := make([]byte, valLen)
	r.file.Read(value)

	return value, true, nil
}
