package sstable

import "errors"

const (
	LEVEL_0_MAXFILESIZE = 1024 * 1    // 1KB
	LEVEL_1_MAXFILESIZE = 1024 * 1024 // 1MB
)

// FileSizeOverError は SSTable のファイルサイズが上限を超えた場合のエラーを表す
var (
	FileSizeOverError = errors.New("SSTable file size exceeded")
)

// シンプルな SSTable レイアウト:
// [Data Block 0][Data Block 1]...[Index Block][Footer]
type indexEntry struct {
	key    []byte
	offset uint32
}

// SSTableMeta は SSTable のメタデータを表す
type SSTableMeta struct {
	// Level uint
	// FilePath string
	Size   uint32
	MinKey []byte
	MaxKey []byte
}

type Version struct{}

func (e *indexEntry) Key() string {
	return string(e.key)
}
