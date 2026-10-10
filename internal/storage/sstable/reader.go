package sstable

import (
	"bytes"
	"distro-storage/internal/storage/action"
	"encoding/binary"
	"io"
	"os"
	"sort"
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
	Act   action.ActType
}

// NewReader は SSTable リーダーを作成します
func NewReader(path string) (*Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	indexOffset, metaOffset, err := readFooter(f)
	if err != nil {
		f.Close()
		return nil, err
	}

	r := &Reader{file: f, dataEnd: indexOffset}
	if err := r.loadIndex(indexOffset, metaOffset); err != nil {
		f.Close()
		return nil, err
	}
	if err := r.loadMeta(metaOffset); err != nil {
		f.Close()
		return nil, err
	}
	return r, nil
}

const footerSize = 12

// readFooter はフッター (indexOffset(4) | metaOffset(4) | magic(4)) を読み込んで検証する
func readFooter(f *os.File) (indexOffset, metaOffset uint32, err error) {
	stat, err := f.Stat()
	if err != nil {
		return 0, 0, err
	}
	size := stat.Size()
	if size < footerSize {
		return 0, 0, os.ErrInvalid
	}

	footer := make([]byte, footerSize)
	if err := readAtFull(f, footer, size-footerSize); err != nil {
		return 0, 0, err
	}
	indexOffset = binary.LittleEndian.Uint32(footer[0:4])
	metaOffset = binary.LittleEndian.Uint32(footer[4:8])
	if binary.LittleEndian.Uint32(footer[8:12]) != 0xABCD {
		return 0, 0, os.ErrInvalid
	}

	// データ → インデックス → メタデータ → フッターの順に並んでいなければ破損
	if indexOffset > metaOffset || int64(metaOffset) > size-footerSize {
		return 0, 0, os.ErrInvalid
	}
	return indexOffset, metaOffset, nil
}

// readAtFull は off から len(buf) バイトを読み込む。足りなければ io.ErrUnexpectedEOF を返す
func readAtFull(f io.ReaderAt, buf []byte, off int64) error {
	n, err := f.ReadAt(buf, off)
	if n == len(buf) {
		return nil
	}
	if err == nil || err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	return err
}

// Next は current の位置からエントリを読み込んで Head() で参照できるようにし、
// current を次のエントリの位置に進める
// 次のエントリが無い場合は false を返す
func (r *Reader) Next() bool {
	if r.current >= r.dataEnd {
		r.head = nil
		return false
	}

	key, value, act, err := readValueAtOffset(r.file, r.current, r.dataEnd)
	if err != nil || !validAct(act) {
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
// インデックスは [indexOffset, metaOffset) の範囲に [KeySize(4)|Key|Offset(4)] が並ぶ
func (r *Reader) loadIndex(indexOffset, metaOffset uint32) error {
	pos, end := uint64(indexOffset), uint64(metaOffset)
	var u32 [4]byte

	for pos < end {
		if pos+4 > end {
			return os.ErrInvalid
		}
		if err := readAtFull(r.file, u32[:], int64(pos)); err != nil {
			return err
		}
		keyLen := uint64(binary.LittleEndian.Uint32(u32[:]))
		pos += 4

		if keyLen+4 > end-pos {
			return os.ErrInvalid
		}
		key := make([]byte, keyLen)
		if err := readAtFull(r.file, key, int64(pos)); err != nil {
			return err
		}
		pos += keyLen

		if err := readAtFull(r.file, u32[:], int64(pos)); err != nil {
			return err
		}
		pos += 4

		r.index = append(r.index, indexEntry{key: key, offset: binary.LittleEndian.Uint32(u32[:])})
	}
	return nil
}

// Get は key に完全一致するエントリの値と action.ActType を返します
//
//   - key が存在しない場合は found=false, err=nil を返す
//   - 削除マーカー(action.ActTypeDelete)も found=true で返す。呼び出し側が action.ActType で判定する
//   - I/O エラーやファイルの破損では err != nil を返す。このとき found は無視してよい
func (r *Reader) Get(key []byte) ([]byte, action.ActType, bool, error) {
	// データが無い、または MinKey/MaxKey の範囲外なら I/O なしで not found
	if r.dataEnd == 0 || bytes.Compare(key, r.meta.MinKey) < 0 || bytes.Compare(key, r.meta.MaxKey) > 0 {
		return nil, 0, false, nil
	}

	// 下限探索: index[i].key <= key を満たす最大の i(key を含み得るブロック)を求める
	i := sort.Search(len(r.index), func(i int) bool {
		return bytes.Compare(r.index[i].key, key) > 0
	}) - 1
	if i < 0 {
		return nil, 0, false, nil
	}

	offset, blockEnd := r.blockRange(i)
	if offset >= blockEnd || blockEnd > r.dataEnd {
		return nil, 0, false, os.ErrInvalid
	}

	// ブロック内を順にデコードしてキーを比較する
	for offset < blockEnd {
		k, value, act, err := readValueAtOffset(r.file, offset, blockEnd)
		if err != nil {
			return nil, 0, false, err
		}
		// [KeySize(4)|Key|ValSize(4)|Val|Act(1)]
		offset += 4 + uint32(len(k)) + 4 + uint32(len(value)) + 1

		switch cmp := bytes.Compare(k, key); {
		case cmp == 0:
			if !validAct(act) {
				return nil, 0, false, os.ErrInvalid
			}
			return value, act, true, nil
		case cmp > 0:
			// ソート済みなので、これ以降に key は存在しない
			return nil, 0, false, nil
		}
	}

	return nil, 0, false, nil
}

// validAct は SSTable に書かれ得る Act (Put/Delete) かどうかを返す
func validAct(act action.ActType) bool {
	return act == action.ActTypePut || act == action.ActTypeDelete
}

// blockRange は i 番目のデータブロックの範囲 [start, end) を返す
// end は次のブロックの先頭、最後のブロックなら dataEnd
func (r *Reader) blockRange(i int) (start, end uint32) {
	start = r.index[i].offset
	if i+1 < len(r.index) {
		return start, r.index[i+1].offset
	}
	return start, r.dataEnd
}

func (r *Reader) Close() error {
	return r.file.Close()
}

// readValueAtOffset は offset から 1 エントリ [KeySize(4)|Key|ValSize(4)|Val|Act(1)] を読み込む
// エントリが limit を超える場合は、長さフィールドの破損とみなして os.ErrInvalid を返す
func readValueAtOffset(f io.ReaderAt, offset, limit uint32) (key, value []byte, actType action.ActType, err error) {
	pos, end := uint64(offset), uint64(limit)
	var u32 [4]byte

	if pos+4 > end {
		return nil, nil, 0, os.ErrInvalid
	}
	if err = readAtFull(f, u32[:], int64(pos)); err != nil {
		return nil, nil, 0, err
	}
	keyLen := uint64(binary.LittleEndian.Uint32(u32[:]))
	pos += 4

	// ValSize(4) も含めて確保前に検査する
	if keyLen+4 > end-pos {
		return nil, nil, 0, os.ErrInvalid
	}
	key = make([]byte, keyLen)
	if err = readAtFull(f, key, int64(pos)); err != nil {
		return nil, nil, 0, err
	}
	pos += keyLen

	if err = readAtFull(f, u32[:], int64(pos)); err != nil {
		return nil, nil, 0, err
	}
	valLen := uint64(binary.LittleEndian.Uint32(u32[:]))
	pos += 4

	// Act(1) も含めて確保前に検査する
	if valLen+1 > end-pos {
		return nil, nil, 0, os.ErrInvalid
	}
	value = make([]byte, valLen)
	if err = readAtFull(f, value, int64(pos)); err != nil {
		return nil, nil, 0, err
	}
	pos += valLen

	var act [1]byte
	if err = readAtFull(f, act[:], int64(pos)); err != nil {
		return nil, nil, 0, err
	}
	return key, value, action.ActType(act[0]), nil
}

// loadMeta は SSTable のメタデータ (Size/MinKey/MaxKey) を読み込む
// メタデータは [metaOffset, フッター) の範囲に [Size(4)|MinKeySize(4)|MinKey|MaxKeySize(4)|MaxKey] が並ぶ
func (r *Reader) loadMeta(metaOffset uint32) error {
	stat, err := r.file.Stat()
	if err != nil {
		return err
	}
	pos, end := uint64(metaOffset), uint64(stat.Size()-footerSize)
	var u32 [4]byte

	readU32 := func() (uint64, error) {
		if pos+4 > end {
			return 0, os.ErrInvalid
		}
		if err := readAtFull(r.file, u32[:], int64(pos)); err != nil {
			return 0, err
		}
		pos += 4
		return uint64(binary.LittleEndian.Uint32(u32[:])), nil
	}
	readKey := func() ([]byte, error) {
		n, err := readU32()
		if err != nil {
			return nil, err
		}
		if n > end-pos {
			return nil, os.ErrInvalid
		}
		key := make([]byte, n)
		if err := readAtFull(r.file, key, int64(pos)); err != nil {
			return nil, err
		}
		pos += n
		return key, nil
	}

	var meta SSTableMeta
	size, err := readU32()
	if err != nil {
		return err
	}
	meta.Size = uint32(size)
	if meta.MinKey, err = readKey(); err != nil {
		return err
	}
	if meta.MaxKey, err = readKey(); err != nil {
		return err
	}

	r.meta = meta
	return nil
}
