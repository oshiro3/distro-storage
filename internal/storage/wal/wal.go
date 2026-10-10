package wal

import (
	"distro-storage/internal/storage/action"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"sync"
)

type LogWriter struct {
	mu   sync.Mutex
	file *os.File
	path string
}

// NewLogWriter は指定されたパスに新しい WAL ファイルを作成し LogWriter を返す
func NewLogWriter(path string) (*LogWriter, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0644)
	if err != nil {
		return nil, err
	}
	return &LogWriter{file: f, path: path}, nil
}

// Write: [CRC(4)|KeySize(4)|ValSize(4)|Type(1)][Key...][Val...]
func (w *LogWriter) Write(entryType action.ActType, key, value []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	kLen, vLen := uint32(len(key)), uint32(len(value))
	header := make([]byte, 13) // 4 + 4 + 4 + 1

	// ペイロード（Key + Value）のチェックサムを計算
	payload := append(key, value...)
	checksum := crc32.ChecksumIEEE(payload)

	binary.LittleEndian.PutUint32(header[0:4], checksum)
	binary.LittleEndian.PutUint32(header[4:8], kLen)
	binary.LittleEndian.PutUint32(header[8:12], vLen)
	header[12] = byte(entryType)

	// ヘッダーとペイロードを書き込み
	if _, err := w.file.Write(header); err != nil {
		return err
	}
	if _, err := w.file.Write(payload); err != nil {
		return err
	}

	// データをディスクにフラッシュ
	return w.file.Sync()
}

// Replay はファイルを最初から読み込み、各エントリに対して引数の関数を適用
func (w *LogWriter) Replay(apply func(entryType action.ActType, key, value []byte)) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	// 読み取りのためにファイルの先頭にシークする
	if _, err := w.file.Seek(0, 0); err != nil {
		return err
	}

	for {
		header := make([]byte, 13)
		_, err := io.ReadFull(w.file, header)
		if err == io.EOF {
			break // ファイルの終端
		}
		if err != nil {
			return fmt.Errorf("failed to read header: %w", err)
		}

		checksum := binary.LittleEndian.Uint32(header[0:4])
		kLen := binary.LittleEndian.Uint32(header[4:8])
		vLen := binary.LittleEndian.Uint32(header[8:12])
		entryType := action.ActType(header[12])

		payload := make([]byte, kLen+vLen)
		if _, err := io.ReadFull(w.file, payload); err != nil {
			return fmt.Errorf("failed to read payload: %w", err)
		}

		// 破損チェック
		if crc32.ChecksumIEEE(payload) != checksum {
			return fmt.Errorf("data corruption detected (checksum mismatch)")
		}

		key := payload[:kLen]
		value := payload[kLen:]
		apply(entryType, key, value)
	}

	// 書き込みに戻るため末尾にシークし直す
	_, err := w.file.Seek(0, 2)
	return err
}

func (w *LogWriter) Close() error {
	return w.file.Close()
}
