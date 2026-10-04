package storage

import (
	"distro-storage/internal/storage/memtable"
	"distro-storage/internal/storage/sstable"
	"distro-storage/internal/storage/wal"
	"errors"
	"log"
	"os"
	"path/filepath"

	"fmt"
	"sync"
)

const MemtableThreshold = 4 * 1024 // 動作確認として 4KB に設定

type Engine struct {
	mu           sync.RWMutex
	dir          string
	activeMem    *memtable.Memtable // 現在書き込み中の Memtable
	immutableMem *memtable.Memtable // Flush中のMemtable
	wal          *wal.LogWriter
	sstCount     int
}

func NewEngine(dir string) (*Engine, error) {
	walPath := fmt.Sprintf("%s/active.wal", dir)
	w, err := wal.NewLogWriter(walPath)
	if err != nil {
		return nil, err
	}

	engine := &Engine{
		activeMem: memtable.NewMemtable(),
		dir:       dir,
		wal:       w, // TODO: ファイル名を連番にする
	}

	// WAL からのリプレイで Memtable を復元
	err = engine.wal.Replay(func(et wal.EntryType, key, value []byte) {
		if et == wal.PutType {
			// Memtable はメモリ上なので、ここでの key/value はコピーして保持
			k := make([]byte, len(key))
			v := make([]byte, len(value))
			copy(k, key)
			copy(v, value)
			engine.activeMem.Put(k, v, memtable.ActType(et))
		}
	})

	return engine, err
}

func (e *Engine) Put(key, value []byte) error {
	e.mu.Lock()

	// WAL に書く
	if err := e.wal.Write(wal.PutType, key, value); err != nil {
		e.mu.Unlock()
		return err
	}
	defer e.mu.Unlock()

	// Memtable に書く
	e.activeMem.Put(key, value, memtable.ActTypePut)

	// サイズチェック
	// 閾値超過で Flush トリガー
	if e.activeMem.Size() > MemtableThreshold && e.immutableMem == nil {
		log.Println("Memtable threshold exceeded, flushing to SSTable...")
		e.immutableMem = e.activeMem
		e.activeMem = memtable.NewMemtable()

		//本来はここで WAL も切り替えるが、今回は簡略化して
		// ImmutableMem を Flush するスレッドを起動
		go e.flushImmutableMemtable()
	}
	return nil
}

// Delete は指定されたキーを削除する
func (e *Engine) Delete(key []byte) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	// WAL に書く
	if err := e.wal.Write(wal.DeleteType, key, nil); err != nil {
		return err
	}

	// Memtable に削除マーカー()を追加
	e.activeMem.Put(key, nil, memtable.ActTypeDelete)

	// Delete ではサイズチェックは不要

	return nil
}

// immutableMemtable を SSTable にフラッシュ
func (e *Engine) flushImmutableMemtable() {
	e.mu.Lock()
	mem := e.immutableMem
	e.sstCount++
	path := e.l0Dir()
	e.mu.Unlock()

	writer, err := sstable.NewWriter(path, sstFileName(e.sstCount))
	if err != nil {
		log.Printf("Error creating SSTable: %v\n", err)
		return
	}

	// Memtable から全エントリを取り出して SSTable へ
	for _, entry := range mem.AllEntries() {
		// 1. まず現在のWriterへの書き込みを試みる
		err := writer.Add(entry.Key, entry.Value, sstable.ActType(entry.Act))

		// NOTE: Recursive にしてもいいが1回しか再帰しないのでシンプルにループで処理する
		if err != nil {
			// エラーが FileSizeOverError かどうかを確認
			if errors.Is(err, sstable.FileSizeOverError) {
				// 2. サイズオーバーの場合は、現在のファイルを書き終えてクローズする
				writer.Finish()

				// 3. 次のファイル名のためにアトミックに sstCount を増やす
				e.mu.Lock()
				e.sstCount++
				e.mu.Unlock()

				// 4. 新しい Writer 作成する
				writer, err = sstable.NewWriter(path, sstFileName(e.sstCount))
				if err != nil {
					log.Printf("Error creating new SSTable during split: %v\n", err)
					return
				}

				// 5. 書き込めなかったエントリを新しいファイルに対して再度書き込む
				err = writer.Add(entry.Key, entry.Value, sstable.ActType(entry.Act))
				if err != nil {
					log.Printf("Fatal error writing to new SSTable (entry too large?): %v\n", err)
					return
				}
			}
		}
	}
	writer.Finish()

	e.mu.Lock()
	e.immutableMem = nil
	e.mu.Unlock()
}

func (e *Engine) Get(key []byte) ([]byte, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	// 新しい層から順に探し、キーが見つかった時点でその層の結果を採用する。
	// 削除マーカーが見つかった場合は、より古い層の値を見ずに not found を返す。

	// まずActive Memtable をチェック
	if val, act, ok := e.activeMem.Get(key); ok {
		if act == memtable.ActTypeDelete {
			return nil, false
		}
		return val, true
	}

	// 次いで Immutable Memtable をチェック
	if e.immutableMem != nil {
		if val, act, ok := e.immutableMem.Get(key); ok {
			if act == memtable.ActTypeDelete {
				return nil, false
			}
			return val, true
		}
	}

	// 最後に SSTables をチェック
	for i := e.sstCount; i >= 1; i-- {
		val, act, ok, err := e.getFromSST(i, key)
		if errors.Is(err, os.ErrNotExist) {
			// sstCount の加算後、ファイルが作られるまでの間に Get が走った場合
			continue
		}
		if err != nil {
			log.Printf("Error reading from SSTable %s: %v\n", sstFileName(i), err)
			return nil, false
		}
		if ok {
			if act == sstable.ActTypeDelete {
				return nil, false
			}
			return val, true
		}
	}
	return nil, false
}

// l0Dir は L0 の SSTable を置くディレクトリを返す (Flush の書き出し先と Get の読み込み先で共通)
func (e *Engine) l0Dir() string {
	return filepath.Join(e.dir, "l0")
}

func sstFileName(n int) string {
	return fmt.Sprintf("%05d.sst", n)
}

// getFromSST は n 番目の SSTable からキーを探す。開いた Reader は必ず閉じる
func (e *Engine) getFromSST(n int, key []byte) ([]byte, sstable.ActType, bool, error) {
	reader, err := sstable.NewReader(filepath.Join(e.l0Dir(), sstFileName(n)))
	if err != nil {
		return nil, 0, false, err
	}
	defer reader.Close()
	return reader.Get(key)
}

func (e *Engine) Close() error {
	return e.wal.Close()
}
