package storage

import (
	"distro-storage/internal/storage/memtable"
	"distro-storage/internal/storage/sstable"
	"distro-storage/internal/storage/wal"
	"log"
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
	path := filepath.Join(e.dir, "l0")
	e.mu.Unlock()

	writer, err := sstable.NewWriter(path, fmt.Sprintf("%05d.sst", e.sstCount))
	if err != nil {
		log.Printf("Error creating SSTable: %v\n", err)
		return
	}

	// Memtable から全エントリを取り出して SSTable へ
	for _, entry := range mem.AllEntries() {
		writer.Add(entry.Key, entry.Value, sstable.ActType(entry.Act))
	}
	writer.Finish()

	e.mu.Lock()
	e.immutableMem = nil
	e.mu.Unlock()
}

func (e *Engine) Get(key []byte) ([]byte, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	// まずActive Memtable をチェック
	if val, act, ok := e.activeMem.Get(key); ok && act != memtable.ActTypeDelete {
		return val, true
	}

	// 次いで Immutable Memtable をチェック
	if e.immutableMem != nil {
		if val, act, ok := e.immutableMem.Get(key); ok && act != memtable.ActTypeDelete {
			return val, true
		}
	}

	// 最後に SSTables をチェック
	for i := e.sstCount; i >= 1; i-- {
		path := filepath.Join(e.dir, fmt.Sprintf("%05d.sst", i))
		reader, _ := sstable.NewReader(path)
		if val, act, ok, err := reader.Get(key); ok {
			if err != nil {
				log.Printf("Error reading from SSTable: %v\n", err)
				return nil, false
			}
			if act != sstable.ActTypeDelete {
				return val, true
			}
		}
	}
	return nil, false
}

func (e *Engine) Close() error {
	return e.wal.Close()
}
