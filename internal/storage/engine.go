package storage

import (
	"bytes"
	"distro-storage/internal/storage/action"
	"distro-storage/internal/storage/manifest"
	"distro-storage/internal/storage/memtable"
	"distro-storage/internal/storage/sstable"
	"distro-storage/internal/storage/wal"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sort"

	"fmt"
	"sync"
)

const MemtableThreshold = 4 * 1024 // 動作確認として 4KB に設定

type Engine struct {
	mu              sync.RWMutex
	dir             string
	activeMem       *memtable.Memtable // 現在書き込み中の Memtable
	immutableMem    *memtable.Memtable // Flush中のMemtable
	wal             *wal.LogWriter     // activeMem 用の WAL (世代ごとに NNNNN.wal)
	walNum          int                // wal の番号
	immutableWALNum int                // immutableMem に対応する WAL の番号
	manifest        *manifest.Manifest // 有効な L0 SSTable の一覧
}

func NewEngine(dir string) (*Engine, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}

	m, err := manifest.Open(filepath.Join(dir, "MANIFEST"))
	if err != nil {
		return nil, err
	}

	engine := &Engine{
		activeMem: memtable.NewMemtable(),
		dir:       dir,
		manifest:  m,
	}

	// 残っている WAL を古い順に再生して Memtable を復元する。
	// 最後の WAL はそのまま書き込み先として使い、WAL が無ければ最初の世代を作る。
	if err := engine.recoverWALs(); err != nil {
		engine.Close()
		return nil, err
	}

	if err := engine.removeOrphanSSTs(); err != nil {
		engine.Close()
		return nil, err
	}

	return engine, nil
}

func walFileName(n int) string {
	return fmt.Sprintf("%05d.wal", n)
}

// listWALs は dir 直下の WAL の番号を昇順で返す。
func listWALs(dir string) ([]int, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.wal"))
	if err != nil {
		return nil, err
	}
	var nums []int
	for _, p := range paths {
		var n int
		if _, err := fmt.Sscanf(filepath.Base(p), "%d.wal", &n); err != nil || filepath.Base(p) != walFileName(n) {
			continue // 自分が作ったものではないファイルは無視する
		}
		nums = append(nums, n)
	}
	sort.Ints(nums)
	return nums, nil
}

// recoverWALs は残っている WAL を古い順に activeMem へ再生し、最新の WAL を e.wal として開く。
func (e *Engine) recoverWALs() error {
	nums, err := listWALs(e.dir)
	if err != nil {
		return err
	}
	if len(nums) == 0 {
		nums = []int{1}
	}

	for _, n := range nums {
		w, err := wal.NewLogWriter(filepath.Join(e.dir, walFileName(n)))
		if err != nil {
			return err
		}
		err = w.Replay(func(act action.ActType, key, value []byte) {
			// Memtable はメモリ上なので、ここでの key/value はコピーして保持
			e.activeMem.Put(bytes.Clone(key), bytes.Clone(value), act)
		})
		if err != nil {
			w.Close()
			return fmt.Errorf("replay %s: %w", walFileName(n), err)
		}
		if n != nums[len(nums)-1] {
			if err := w.Close(); err != nil {
				return err
			}
			continue
		}
		e.wal, e.walNum = w, n
	}
	return nil
}

// rotateWAL は新しい世代の WAL に書き込み先を切り替える。呼び出し側が e.mu を保持していること。
// 失敗した場合は、切り替え前の状態のまま返す。
func (e *Engine) rotateWAL() error {
	next := e.walNum + 1
	w, err := wal.NewLogWriter(filepath.Join(e.dir, walFileName(next)))
	if err != nil {
		return err
	}
	if err := e.wal.Close(); err != nil {
		w.Close()
		return err
	}
	e.wal, e.walNum = w, next
	return nil
}

func (e *Engine) Put(key, value []byte) error {
	e.mu.Lock()

	// WAL に書く
	if err := e.wal.Write(action.ActTypePut, key, value); err != nil {
		e.mu.Unlock()
		return err
	}
	defer e.mu.Unlock()

	// Memtable に書く
	e.activeMem.Put(key, value, action.ActTypePut)

	// サイズチェック
	// 閾値超過で Flush トリガー
	if e.activeMem.Size() > MemtableThreshold && e.immutableMem == nil {
		// Memtable の世代と WAL の世代を揃える。切り替えに失敗したら rotate せず、次の Put で再試行する
		oldWALNum := e.walNum
		if err := e.rotateWAL(); err != nil {
			log.Printf("Error rotating WAL: %v\n", err)
			return nil
		}
		log.Println("Memtable threshold exceeded, flushing to SSTable...")
		e.immutableMem = e.activeMem
		e.immutableWALNum = oldWALNum
		e.activeMem = memtable.NewMemtable()

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
	if err := e.wal.Write(action.ActTypeDelete, key, nil); err != nil {
		return err
	}

	// Memtable に削除マーカー()を追加
	e.activeMem.Put(key, nil, action.ActTypeDelete)

	// Delete ではサイズチェックは不要

	return nil
}

// immutableMemtable を SSTable にフラッシュ
func (e *Engine) flushImmutableMemtable() {
	e.mu.Lock()
	mem := e.immutableMem
	walNum := e.immutableWALNum
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
		err := writer.Add(entry.Key, entry.Value, entry.Act)

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
				err = writer.Add(entry.Key, entry.Value, entry.Act)
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

	// SST が Manifest に載ってから、対応する WAL を削除する。
	// 先に削除すると、削除後にクラッシュしたときに書き込みが失われる。
	// 削除に失敗しても残るだけで、次回起動時の再生が二重になるが結果は変わらない。
	if err := os.Remove(filepath.Join(e.dir, walFileName(walNum))); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("Error removing flushed WAL: %v\n", err)
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
		if act == action.ActTypeDelete {
			return nil, false, nil
		}
		return val, true, nil
	}

	// 次いで Immutable Memtable をチェック
	if e.immutableMem != nil {
		if val, act, ok := e.immutableMem.Get(key); ok {
			if act == action.ActTypeDelete {
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
			if act == action.ActTypeDelete {
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
func (e *Engine) getFromSST(n int, key []byte) ([]byte, action.ActType, bool, error) {
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
