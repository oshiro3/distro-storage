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
	// データブロックの終端(インデックスブロックの開始位置)を指すオフセット
	dataEnd uint32
	// 次に読み込むエントリの位置を指すオフセット
	current uint32
	// 直前の Next() で読み込んだエントリ
	head *Entry
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

// Next は current の位置からエントリを読み込んで Head() で参照できるようにし、
// current を次のエントリの位置に進める
// 次のエントリが無い場合は false を返す
func (r *Reader) Next() bool {
	if r.current >= r.dataEnd {
		r.head = nil
		return false
	}

	key, value, act, err := readValueAtOffset(r.file, r.current)
	if err != nil {
		r.head = nil
		return false
	}

	// [KeySize(4)|Key|ValSize(4)|Val|Act(1)]
	r.current += 4 + uint32(len(key)) + 4 + uint32(len(value)) + 1
	r.head = &Entry{Key: key, Value: value, Act: act}

	return true
}

// Head は直前の Next() で読み込んだ現在のエントリを返す
func (r *Reader) Head() *Entry {
	return r.head
}

// loadIndex は SSTable のインデックスを読み込む
func (r *Reader) loadIndex() error {
	stat, _ := r.file.Stat()
	size := stat.Size()

	// フッターの読み込み (indexOffset(4) | metaOffset(4) | magic(4))
	footer := make([]byte, 12)
	if _, err := r.file.ReadAt(footer, size-12); err != nil {
		return err
	}
	indexOffset := binary.LittleEndian.Uint32(footer[0:4])
	metaOffset := binary.LittleEndian.Uint32(footer[4:8])
	magic := binary.LittleEndian.Uint32(footer[8:12])
	if magic != 0xABCD {
		return os.ErrInvalid
	}
	r.dataEnd = indexOffset

	// インデックスブロックの読み込み
	_, err := r.file.Seek(int64(indexOffset), io.SeekStart)
	if err != nil {
		return err
	}

	for {
		curr, _ := r.file.Seek(0, io.SeekCurrent)
		if curr >= int64(metaOffset) {
			break
		}
		var keyLen uint32
		if err := binary.Read(r.file, binary.LittleEndian, &keyLen); err != nil {
			return err
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
