// Package action は、WAL / memtable / sstable で共通に使うレコード種別を定義する。
package action

// ActType はレコードの種別。WAL と SSTable のオンディスク形式 (1 バイト) に書かれるため、値を変えてはならない。
// 0 は未使用にして、ゼロ値や未初期化を Put と誤認しないようにする。
type ActType byte

const (
	ActTypePut    ActType = 1
	ActTypeGet    ActType = 2
	ActTypeDelete ActType = 3
)
