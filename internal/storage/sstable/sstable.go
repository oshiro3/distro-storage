package sstable

const (
	LEVEL0_MAXFILES = 4
	LEVEL1_MAXFILES = 4
)

// シンプルな SSTable レイアウト:
// [Data Block 0][Data Block 1]...[Index Block][Footer]
type indexEntry struct {
	key    []byte
	offset uint32
}

// SSTableMeta は SSTable のメタデータを表す
// SSTable とレベルの関係などを管理する
type SSTableMeta struct {
	Level    uint
	FilePath string
	FileSize int64
	MinKey   string
	MaxKey   string
}

type Version struct{}

func (e *indexEntry) Key() string {
	return string(e.key)
}
