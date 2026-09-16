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
	meta  SSTableMeta
	// 現在のエントリの位置を指すオフセット
	current uint32
}

// レコードの実体構造体
type Entry struct {
	Key   []byte
	Value []byte
	Act   ActType
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
	if err := r.loadMeta(); err != nil {
		f.Close()
		return nil, err
	}
	return r, nil
}

// Next は current を更新して次のエントリに進める
// 次のエントリが無い場合は false を返す
func (r *Reader) Next() bool {
	if _, err := r.file.Seek(int64(r.current), io.SeekStart); err != nil {
		log.Printf("Error seeking to current offset: %v\n", err)
		return false
	}
	var keyLen uint32
	if err := binary.Read(r.file, binary.LittleEndian, &keyLen); err != nil {
		return false
	}

	var valLen uint32
	if err := binary.Read(r.file, binary.LittleEndian, &valLen); err != nil {
		return false
	}

	// ActType は1バイト固定
	r.current += keyLen + valLen + 1

	return true
}

// Head はOFFSETがある場合現在のエントリを返す
func (r *Reader) Head() *Entry {
	if r.current == 0 {
		log.Println("Current offset is 0, no entry to read")
		return nil
	}
	key, value, act, err := readValueAtOffset(r.file, r.current)
	if err != nil {
		log.Printf("Error reading value at offset %d: %v\n", r.current, err)
		return &Entry{}
	}

	return &Entry{key, value, act}
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
func (r *Reader) Get(key []byte) ([]byte, ActType, bool, error) {
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

	_, value, act, err := readValueAtOffset(r.file, targetOffset)
	if err != nil {
		return nil, 0, false, err
	}

	return value, act, true, nil
}

func (r *Reader) Close() error {
	return r.file.Close()
}

func readValueAtOffset(f *os.File, targetOffset uint32) (key, value []byte, actType ActType, err error) {
	// データブロックから値を読み込み
	if _, err = f.Seek(int64(targetOffset), io.SeekStart); err != nil {
		return
	}

	var keyLen uint32
	if err = binary.Read(f, binary.LittleEndian, &keyLen); err != nil {
		return
	}
	key = make([]byte, keyLen)
	f.Read(key)

	var valLen uint32
	if err = binary.Read(f, binary.LittleEndian, &valLen); err != nil {
		return
	}
	value = make([]byte, valLen)
	f.Read(value)

	var act [1]byte
	if _, err = io.ReadFull(f, act[:]); err != nil {
		return
	}
	return key, value, ActType(act[0]), nil
}

// loadMeta は SSTable のメタデータ (Size/MinKey/MaxKey) を読み込む
func (r *Reader) loadMeta() error {
	stat, err := r.file.Stat()
	if err != nil {
		return err
	}
	size := stat.Size()

	// フッターの読み込み (indexOffset(4) | metaOffset(4) | magic(4))
	footer := make([]byte, 12)
	if _, err := r.file.ReadAt(footer, size-12); err != nil {
		return err
	}
	metaOffset := binary.LittleEndian.Uint32(footer[4:8])
	magic := binary.LittleEndian.Uint32(footer[8:12])
	if magic != 0xABCD {
		return os.ErrInvalid
	}

	if _, err := r.file.Seek(int64(metaOffset), io.SeekStart); err != nil {
		return err
	}

	var meta SSTableMeta
	if err := binary.Read(r.file, binary.LittleEndian, &meta.Size); err != nil {
		return err
	}

	var minKeyLen uint32
	if err := binary.Read(r.file, binary.LittleEndian, &minKeyLen); err != nil {
		return err
	}
	meta.MinKey = make([]byte, minKeyLen)
	if _, err := io.ReadFull(r.file, meta.MinKey); err != nil {
		return err
	}

	var maxKeyLen uint32
	if err := binary.Read(r.file, binary.LittleEndian, &maxKeyLen); err != nil {
		return err
	}
	meta.MaxKey = make([]byte, maxKeyLen)
	if _, err := io.ReadFull(r.file, meta.MaxKey); err != nil {
		return err
	}

	r.meta = meta
	return nil
}
