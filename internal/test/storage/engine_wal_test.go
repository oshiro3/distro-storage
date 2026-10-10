package storage_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"distro-storage/internal/storage"
	"distro-storage/internal/storage/action"
	"distro-storage/internal/storage/wal"
)

// issue #6: WAL が切り替え・削除されず、Flush 済みデータも毎回再生される問題の受入テスト。
//
// 公開 API とディレクトリ上のファイルだけで検証する。前提とする仕様は次の 2 点。
//   - WAL は dir 直下の "*.wal" で、Memtable の世代ごとに 1 ファイル (例: 00001.wal)
//   - 起動時は、削除されていない WAL を名前 (連番) の昇順で再生する
//
// 注意: engine_restart_test.go の restart() は全 WAL を削除して再起動する。
// このファイルのテストは WAL を触らずに再起動する reopen() を使う。

// walFiles は dir 直下の WAL ファイル名を昇順で返す。
func walFiles(t *testing.T, dir string) []string {
	t.Helper()

	paths, err := filepath.Glob(filepath.Join(dir, "*.wal"))
	if err != nil {
		t.Fatalf("glob wal: %v", err)
	}
	names := make([]string, len(paths))
	for i, p := range paths {
		names[i] = filepath.Base(p)
	}
	return names
}

// reopen は e を閉じ、WAL を含めて何も手を加えずに同じディレクトリでエンジンを開き直す。
func reopen(t *testing.T, e *testEngine) *testEngine {
	t.Helper()

	if err := e.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
	e2, err := storage.NewEngine(e.dir)
	if err != nil {
		t.Fatalf("NewEngine() after reopen error: %v", err)
	}
	t.Cleanup(func() { e2.Close() })
	return &testEngine{Engine: e2, dir: e.dir}
}

// eventually は cond が true になるまで d の間ポーリングし、true になったかを返す。
// WAL の削除は Flush 完了後に非同期で行われるため、すぐには確認できない。
func eventually(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// walSize は WAL ファイルの合計サイズを返す。
func walSize(t *testing.T, dir string) int64 {
	t.Helper()

	var total int64
	for _, name := range walFiles(t, dir) {
		if fi, err := os.Stat(filepath.Join(dir, name)); err == nil {
			total += fi.Size()
		}
	}
	return total
}

// walContains は、WAL ファイルのいずれかが needle を含むかを返す。
func walContains(t *testing.T, dir string, needle []byte) bool {
	t.Helper()

	for _, name := range walFiles(t, dir) {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue // 読んでいる間に削除された
		}
		if bytes.Contains(b, needle) {
			return true
		}
	}
	return false
}

// --- 受入条件 1: Flush 後に、対応する WAL が削除される ---

// Flush した Memtable に対応する WAL は削除される。書き込み中の WAL は残る。
func TestEngine_WAL_FlushedWALIsDeleted(t *testing.T) {
	e := newTestEngine(t)

	e.put(t, "a", "1")
	e.put(t, "b", "2")
	old := walFiles(t, e.dir)
	if len(old) == 0 {
		t.Fatal("precondition: no WAL file exists after Put")
	}

	e.flush(t)

	gone := eventually(5*time.Second, func() bool {
		cur := make(map[string]bool)
		for _, n := range walFiles(t, e.dir) {
			cur[n] = true
		}
		for _, n := range old {
			if cur[n] {
				return false
			}
		}
		return true
	})
	if !gone {
		t.Errorf("WAL files %v still exist after the flush, want them deleted (now: %v)", old, walFiles(t, e.dir))
	}
	if len(walFiles(t, e.dir)) == 0 {
		t.Error("no WAL file exists after the flush, want a WAL for the active memtable")
	}

	// WAL を消しても、Flush 済みのデータは SST から読める
	e.assertFound(t, "a", "1")
	e.assertFound(t, "b", "2")
}

// Flush を繰り返しても WAL ファイルが溜まらない (起動時間が履歴に比例して伸びない)。
func TestEngine_WAL_DoesNotAccumulate(t *testing.T) {
	e := newTestEngine(t)

	const rounds = 4
	for i := range rounds {
		e.put(t, "k", string(rune('a'+i)))
		e.flush(t)
	}
	if n := e.sstCount(); n < rounds {
		t.Fatalf("precondition: SST count = %d, want >= %d", n, rounds)
	}

	// 残ってよいのは、書き込み中の WAL と、Flush 中の Memtable の WAL まで。
	// ファイル数は単一ファイルのままでも満たせるため、合計サイズで見る (Flush 済みの分が残っていれば閾値を大きく超える)。
	limit := int64(3 * storage.MemtableThreshold)
	ok := eventually(5*time.Second, func() bool { return walSize(t, e.dir) <= limit })
	if !ok {
		t.Errorf("WAL total size = %d bytes (%v) after %d flushes, want <= %d", walSize(t, e.dir), walFiles(t, e.dir), rounds, limit)
	}
}

// --- 受入条件 2: 再起動時に Flush 済みデータが再投入されない ---

// Flush 済みのデータは WAL に残らず、再起動後の書き込みで再 Flush も起きない。
func TestEngine_WAL_RestartDoesNotReplayFlushedData(t *testing.T) {
	e := newTestEngine(t)

	marker := []byte("MARKER-VALUE-ONLY-IN-FLUSHED-DATA")
	e.put(t, "marker", string(marker))
	e.flush(t)

	// WAL の削除を待つ (削除されない場合は、下の検証で失敗する)
	eventually(5*time.Second, func() bool { return !walContains(t, e.dir, marker) })

	e2 := reopen(t, e)
	before := e2.sstCount()

	if walContains(t, e2.dir, marker) {
		t.Error("flushed data is still in a WAL file after restart, want it deleted with the flushed memtable")
	}

	// Flush 済みデータが Memtable に戻っていると、閾値を超えた状態になり、
	// 小さな Put 1 件で再び Flush が起きて SST が増える
	e2.put(t, "after", "x")
	if eventually(time.Second, func() bool { return e2.sstCount() > before }) {
		t.Errorf("SST count grew from %d to %d after one small Put, want no flush (flushed data was replayed into the memtable)", before, e2.sstCount())
	}

	e2.assertFound(t, "marker", string(marker))
	e2.assertFound(t, "after", "x")
}

// WAL が残っていても、再起動後に Flush 済みの削除が覆らない (古い Put の再生で削除済みキーが復活しない)。
func TestEngine_WAL_RestartKeepsDeleteOfFlushedKey(t *testing.T) {
	e := newTestEngine(t)

	e.put(t, "k", "v")
	e.put(t, "other", "o")
	e.flush(t)
	e.del(t, "k")
	e.flush(t)

	e2 := reopen(t, e)

	e2.assertNotFound(t, "k")
	e2.assertFound(t, "other", "o")
}

// Flush 前の書き込みは、WAL から復元される (WAL を消しすぎない)。
func TestEngine_WAL_UnflushedWritesSurviveRestart(t *testing.T) {
	e := newTestEngine(t)

	e.put(t, "flushed", "1")
	e.flush(t)
	// Flush の後の書き込み。まだ SST には無い
	e.put(t, "unflushed", "2")
	e.del(t, "flushed")

	e2 := reopen(t, e)

	e2.assertFound(t, "unflushed", "2")
	e2.assertNotFound(t, "flushed")
}

// --- 受入条件 3: rotate 直後にクラッシュしても、どの書き込みも失われない ---

// writeWAL は dir に WAL ファイルを作り、records を順に書く。クラッシュ後のディレクトリを再現するために使う。
func writeWAL(t *testing.T, dir, name string, records []walRecord) {
	t.Helper()

	w, err := wal.NewLogWriter(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("NewLogWriter(%s) error: %v", name, err)
	}
	defer w.Close()
	for _, r := range records {
		if err := w.Write(r.act, []byte(r.key), []byte(r.value)); err != nil {
			t.Fatalf("Write(%s, %q) error: %v", name, r.key, err)
		}
	}
}

type walRecord struct {
	act        action.ActType
	key, value string
}

func put(k, v string) walRecord { return walRecord{action.ActTypePut, k, v} }
func del(k string) walRecord    { return walRecord{action.ActTypeDelete, k, ""} }

// rotate した直後にクラッシュし、古い WAL の Flush が終わっていない状態 (SST なし、WAL が 2 世代) から、
// 全ての書き込みが古い順に再生される。
func TestEngine_WAL_CrashAfterRotateLosesNothing(t *testing.T) {
	dir := t.TempDir()

	// 古い世代: Flush 前にクラッシュしたので、SST も Manifest も無い
	writeWAL(t, dir, "00001.wal", []walRecord{
		put("a", "old"),
		put("b", "old"),
		put("only-old", "1"),
	})
	// 新しい世代: rotate 後の書き込み。古い世代のキーを上書き・削除している
	writeWAL(t, dir, "00002.wal", []walRecord{
		put("a", "new"),
		del("b"),
		put("only-new", "2"),
	})

	e, err := storage.NewEngine(dir)
	if err != nil {
		t.Fatalf("NewEngine() error: %v, want the engine to start from the two WAL files", err)
	}
	t.Cleanup(func() { e.Close() })
	te := &testEngine{Engine: e, dir: dir}

	te.assertFound(t, "a", "new") // 新しい世代が勝つ
	te.assertNotFound(t, "b")     // 新しい世代の Delete が勝つ
	te.assertFound(t, "only-old", "1")
	te.assertFound(t, "only-new", "2")
}

// 起動後の書き込みも失われない。複数世代の WAL を再生した後に再起動を繰り返しても、結果は変わらない。
func TestEngine_WAL_CrashAfterRotate_SurvivesFurtherRestarts(t *testing.T) {
	dir := t.TempDir()

	writeWAL(t, dir, "00001.wal", []walRecord{put("a", "old"), put("b", "old")})
	writeWAL(t, dir, "00002.wal", []walRecord{put("a", "new")})

	e, err := storage.NewEngine(dir)
	if err != nil {
		t.Fatalf("NewEngine() error: %v", err)
	}
	t.Cleanup(func() { e.Close() })
	te := &testEngine{Engine: e, dir: dir}
	te.put(t, "c", "after-recovery")

	for range 2 {
		te = reopen(t, te)
		te.assertFound(t, "a", "new")
		te.assertFound(t, "b", "old")
		te.assertFound(t, "c", "after-recovery")
	}
}
