package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const visibleRunLogLimit = 256 * 1024

type runLog struct {
	mu        sync.Mutex
	file      *os.File
	path      string
	lines     []string
	size      int
	truncated bool
	version   uint64
}

func openLastRunLog() (*runLog, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return newRunLog("", nil), fmt.Errorf("find user cache directory: %w", err)
	}
	path := filepath.Join(dir, "yave", "last-conversion.log")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return newRunLog("", nil), fmt.Errorf("create log directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return newRunLog("", nil), fmt.Errorf("open conversion log: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return newRunLog("", nil), fmt.Errorf("secure conversion log: %w", err)
	}
	return newRunLog(path, file), nil
}

func newRunLog(path string, file *os.File) *runLog {
	return &runLog{path: path, file: file}
}

func (l *runLog) AppendLine(line string) {
	if l == nil {
		return
	}
	if !strings.HasSuffix(line, "\n") {
		line += "\n"
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		_, _ = l.file.WriteString(line)
	}
	l.lines = append(l.lines, line)
	l.size += len(line)
	for l.size > visibleRunLogLimit && len(l.lines) > 1 {
		l.size -= len(l.lines[0])
		l.lines = l.lines[1:]
		l.truncated = true
	}
	if l.size > visibleRunLogLimit && len(l.lines) == 1 {
		overflow := l.size - visibleRunLogLimit
		l.lines[0] = trimUTF8Prefix(l.lines[0], overflow)
		l.size = len(l.lines[0])
		l.truncated = true
	}
	l.version++
}

func (l *runLog) Snapshot() (string, uint64) {
	if l == nil {
		return "", 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	var notices []string
	if l.truncated {
		notices = append(notices, "Earlier output omitted from this view.")
	}
	if l.path != "" {
		notices = append(notices, "Complete log: "+l.path)
	}
	var prefix string
	if len(notices) > 0 {
		prefix = strings.Join(notices, " ") + "\n\n"
	}
	return prefix + strings.Join(l.lines, ""), l.version
}

func (l *runLog) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

func trimUTF8Prefix(value string, count int) string {
	if count >= len(value) {
		return ""
	}
	value = value[count:]
	for len(value) > 0 && value[0]&0xc0 == 0x80 {
		value = value[1:]
	}
	return value
}
