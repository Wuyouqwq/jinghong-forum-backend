package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"jinghong-forum-backend/internal/app"
)

func main() {
	_ = os.MkdirAll("logs", 0o755)
	logFile, err := os.OpenFile(filepath.Join("logs", "app.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		panic(err)
	}
	defer logFile.Close()
	logger := slog.New(slog.NewJSONHandler(io.MultiWriter(os.Stdout, logFile), nil))
	slog.SetDefault(logger)

	cfg := app.LoadConfig()
	server, err := app.NewServer(context.Background(), cfg)
	if err != nil {
		logger.Error("initialize server", "error", err)
		os.Exit(1)
	}

	errCh := make(chan error, 1)
	go func() { errCh <- server.Start() }()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-errCh:
		logger.Error("server stopped", "error", err)
	case <-stop:
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}
}
