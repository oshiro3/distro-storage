package sstable

import (
	"bytes"
	"container/heap"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"path/filepath"
)

// Compact は指定されたレベルの SSTable をコンパクト化する
// path はデータディレクトリ
func Compact(ctx context.Context, path string, count *uint, level uint) error {
	// level0のコンパクトは全てのL0ファイルを対象にする
	// NOTE: 効率としては古い順に一定数ずつコンパクトする方が良いが今回は全てのファイルを対象にする
	var iters []Iterator
	var targets []string
	if level == 0 {
		// 全てのL0ファイルを読み込むイテレータを作成
		// 1. L0ファイルのリストを取得
		l0Path := filepath.Join(path, "l0")
		if err := filepath.Walk(l0Path, func(path string, info fs.FileInfo, err error) error {
			if info.IsDir() {
				return nil
			}
			reader, _ := NewReader(path)
			targets = append(targets, path)
			return nil
		}); err != nil {
			return err
		}

		// 2. minKey と maxKey を取得

		// 3. L1のインデックスを見てCompact対象になるファイルを特定
		// 4. L0のイテレータとL1のイテレータを作成
		iters := make([]Iterator, len(targets))
		for _, path := range targets {
			reader, err := NewReader(path)
			if err != nil {
				log.Printf("Error creating reader for %s: %v\n", path, err)
				return err
			}
			iters = append(iters, reader)
		}
	} else {
	}

	if err := compact(ctx, iters, path, count); err != nil {
		log.Printf("Error during compaction: %v\n", err)
		return err
	}

	return nil
}

func compact(ctx context.Context, iters []Iterator, path string, count *uint) error {
	pq := make(PriorityQueue, 0, len(iters))

	// イテレータを初期化してヒープに追加
	for i, iter := range iters {
		if iter.Next() {
			pq = append(pq, &Item{Iter: iter, Priority: i})
		}
	}

	// NOTE: count のロックが取れない問題がある
	writer, err := NewWriter(path, fmt.Sprintf("%05d.sst", count))
	if err != nil {
		log.Printf("Error creating new SSTable during split: %v\n", err)
		return err
	}
	// マージループ
	for pq.Len() > 0 {
		// 最小の要素を取得
		minItem := heap.Pop(&pq).(*Item)
		currentKey := minItem.Iter.Head().Key
		currentVal := minItem.Iter.Head().Value
		currentAct := minItem.Iter.Head().Act

		// 同じキーの要素をすべて処理
		for pq.Len() > 0 && bytes.Equal(pq[0].Iter.Head().Key, currentKey) {
			oldItem := heap.Pop(&pq).(*Item)
			// イテレータを進めて残ったデータをヒープに戻す
			if oldItem.Iter.Next() {
				heap.Push(&pq, oldItem)
			}
		}

		// 最新のデータを新しいSSTableに書き込む
		err := writer.Add(currentKey, currentVal, currentAct)
		// ファイルは最大サイズを考慮して書き込みを分割する必要がある
		if err != nil {
			if errors.Is(err, FileSizeOverError) {
				// サイズオーバーの場合は、現在のファイルを書き終えてクローズする
				writer.Finish()
				// 次のファイル名のためにアトミックに sstCount を増やす
				*count++
				writer, err = NewWriter(path, fmt.Sprintf("%05d.sst", count))
				if err != nil {
					log.Printf("Error creating new SSTable during split: %v\n", err)
					return err
				}

				// 書き込めなかったエントリを新しいファイルに対して再度書き込む
				if err := writer.Add(currentKey, currentVal, currentAct); err != nil {
					log.Printf("Fatal error writing to new SSTable (entry too large?): %v\n", err)
					return err
				}
			}
		}

		// 現在のイテレータを進めて残ったデータをヒープに戻す
		if minItem.Iter.Next() {
			heap.Push(&pq, minItem)
		}
	}

	return writer.Finish()
}
