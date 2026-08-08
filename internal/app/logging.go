package app

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"
)

type rotatingLogWriter struct {
	mu         sync.Mutex
	root       *os.Root
	name       string
	file       *os.File
	size       int64
	maxSize    int64
	maxBackups int
	maxAge     time.Duration
}

func NewLogger(cfg Config) (*slog.Logger, io.Closer, error) {
	cfg = cfg.withDefaults()
	if err := os.MkdirAll(cfg.LogDir, 0o750); err != nil {
		return nil, nil, fmt.Errorf("create log directory: %w", err)
	}
	root, err := os.OpenRoot(cfg.LogDir)
	if err != nil {
		return nil, nil, fmt.Errorf("open log directory: %w", err)
	}
	writer, err := newRotatingLogWriter(root, "app.log", cfg.LogMaxSizeMB, cfg.LogMaxBackups, cfg.LogMaxAgeDays)
	if err != nil {
		_ = root.Close()
		return nil, nil, err
	}
	level := slog.LevelInfo
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	logger := slog.New(slog.NewJSONHandler(io.MultiWriter(os.Stdout, writer), &slog.HandlerOptions{Level: level}))
	return logger, writer, nil
}

func newRotatingLogWriter(root *os.Root, name string, maxSizeMB, maxBackups, maxAgeDays int) (*rotatingLogWriter, error) {
	file, err := root.OpenFile(name, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("stat log file: %w", err)
	}
	return &rotatingLogWriter{root: root, name: name, file: file, size: info.Size(), maxSize: int64(maxSizeMB) << 20, maxBackups: maxBackups, maxAge: time.Duration(maxAgeDays) * 24 * time.Hour}, nil
}

func (w *rotatingLogWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil || w.root == nil {
		return 0, errors.New("log writer is closed")
	}
	if w.size > 0 && w.size+int64(len(data)) > w.maxSize {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	written, err := w.file.Write(data)
	w.size += int64(written)
	return written, err
}

func (w *rotatingLogWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	var result error
	if w.file != nil {
		result = w.file.Close()
		w.file = nil
	}
	if w.root != nil {
		result = errors.Join(result, w.root.Close())
		w.root = nil
	}
	return result
}

func (w *rotatingLogWriter) rotate() error {
	if err := w.file.Close(); err != nil {
		return w.reopenAfterRotationFailure(err)
	}
	w.file = nil
	for index := w.maxBackups; index >= 1; index-- {
		name := fmt.Sprintf("%s.%d", w.name, index)
		if index == w.maxBackups {
			_ = w.root.Remove(name)
			continue
		}
		next := fmt.Sprintf("%s.%d", w.name, index+1)
		if err := w.root.Rename(name, next); err != nil && !os.IsNotExist(err) {
			return w.reopenAfterRotationFailure(err)
		}
	}
	if err := w.root.Rename(w.name, w.name+".1"); err != nil && !os.IsNotExist(err) {
		return w.reopenAfterRotationFailure(err)
	}
	file, err := w.root.OpenFile(w.name, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return w.reopenAfterRotationFailure(err)
	}
	w.file, w.size = file, 0
	w.removeExpiredBackups()
	return nil
}

func (w *rotatingLogWriter) reopenAfterRotationFailure(rotationErr error) error {
	file, openErr := w.root.OpenFile(w.name, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if openErr != nil {
		return errors.Join(rotationErr, fmt.Errorf("reopen active log: %w", openErr))
	}
	info, statErr := file.Stat()
	if statErr != nil {
		_ = file.Close()
		return errors.Join(rotationErr, fmt.Errorf("stat reopened log: %w", statErr))
	}
	w.file, w.size = file, info.Size()
	return rotationErr
}

func (w *rotatingLogWriter) removeExpiredBackups() {
	if w.maxAge <= 0 {
		return
	}
	cutoff := time.Now().Add(-w.maxAge)
	for index := 1; index <= w.maxBackups; index++ {
		name := fmt.Sprintf("%s.%d", w.name, index)
		info, err := w.root.Stat(name)
		if err == nil && info.ModTime().Before(cutoff) {
			_ = w.root.Remove(name)
		}
	}
}

func (s *Server) warnRedis(message string, args ...any) {
	now := time.Now()
	s.redisWarnMu.Lock()
	if now.Sub(s.redisWarnAt) < time.Minute {
		s.redisWarnMu.Unlock()
		return
	}
	s.redisWarnAt = now
	s.redisWarnMu.Unlock()
	slog.Warn(message, args...)
}
