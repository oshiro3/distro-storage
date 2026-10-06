package storage

import (
	"distro-storage/internal/storage/manifest"
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
	manifest     *manifest.Manifest // 有効な L0 SSTable の一覧
}

func NewEngine(dir string) (*Engine, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}

	m, err := manifest.Open(filepath.Join(dir, "MANIFEST"))
	if err != nil {
		return nil, err
	}

	walPath := fmt.Sprintf("%s/active.wal", dir)
	w, err := wal.NewLogWriter(walPath)
	if err != nil {
		m.Close()
		return nil, err
	}

	engine := &Engine{
		activeMem: memtable.NewMemtable(),
		dir:       dir,
		wal:       w, // TODO: ファイル名を連番にする
		manifest:  m,
	}

	if err := engine.removeOrphanSSTs(); err != nil {
		engine.Close()
		return nil, err
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
	num := e.manifest.NextFileNum()
	path := e.l0Dir()
	e.mu.Unlock()

	writer, err := sstable.NewWriter(path, sstFileName(num))
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
				if err := e.finishSST(writer, num); err != nil {
					log.Printf("Error finishing SSTable during split: %v\n", err)
					return
				}

				// 3. 次のファイル名のために番号を払い出す
				num = e.manifest.NextFileNum()

				// 4. 新しい Writer 作成する
				writer, err = sstable.NewWriter(path, sstFileName(num))
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
	if err := e.finishSST(writer, num); err != nil {
		log.Printf("Error finishing SSTable: %v\n", err)
		return
	}

	e.mu.Lock()
	e.immutableMem = nil
	e.mu.Unlock()
}

// Get は key の最新の値を返す。
// 見つからない、または削除済みの場合は found=false, err=nil を返す。
// SSTable の読み込みに失敗した場合は err != nil を返す (このとき found は無視してよい)。
func (e *Engine) Get(key []byte) (value []byte, found bool, err error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	// 新しい層から順に探し、キーが見つかった時点でその層の結果を採用する。
	// 削除マーカーが見つかった場合は、より古い層の値を見ずに not found を返す。

	// まずActive Memtable をチェック
	if val, act, ok := e.activeMem.Get(key); ok {
		if act == memtable.ActTypeDelete {
			return nil, false, nil
		}
		return val, true, nil
	}

	// 次いで Immutable Memtable をチェック
	if e.immutableMem != nil {
		if val, act, ok := e.immutableMem.Get(key); ok {
			if act == memtable.ActTypeDelete {
				return nil, false, nil
			}
			return val, true, nil
		}
	}

	// 最後に SSTables をチェック
	// Manifest に載っているのは完成済みの SST だけ (新しい順)
	for _, i := range e.manifest.Files() {
		val, act, ok, err := e.getFromSST(i, key)
		if err != nil {
			return nil, false, fmt.Errorf("read SSTable %s: %w", sstFileName(i), err)
		}
		if ok {
			if act == sstable.ActTypeDelete {
				return nil, false, nil
			}
			return val, true, nil
		}
	}
	return nil, false, nil
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

// finishSST は SSTable を完成させ、Manifest に記録して有効にする。
// Manifest に載るまでは Get から見えず、途中でクラッシュしても孤児ファイルが残るだけで済む。
func (e *Engine) finishSST(w *sstable.Writer, num int) error {
	if err := w.Finish(); err != nil {
		return err
	}
	return e.manifest.AddFile(num)
}

// removeOrphanSSTs は Manifest に載っていない SST (Flush 途中でクラッシュした残骸) を削除する。
func (e *Engine) removeOrphanSSTs() error {
	valid := make(map[string]bool)
	for _, n := range e.manifest.Files() {
		valid[sstFileName(n)] = true
	}

	paths, err := filepath.Glob(filepath.Join(e.l0Dir(), "*.sst"))
	if err != nil {
		return err
	}
	for _, p := range paths {
		if !valid[filepath.Base(p)] {
			if err := os.Remove(p); err != nil {
				return err
			}
		}
	}
	return nil
}

func (e *Engine) Close() error {
	return errors.Join(e.wal.Close(), e.manifest.Close())
}
