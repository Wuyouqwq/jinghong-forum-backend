package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"jinghong-forum-backend/internal/app"
)

func main() {
	os.Exit(run())
}

func run() int {
	cfg := app.LoadConfig()
	if err := cfg.Validate(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "invalid configuration: %v\n", err)
		return 1
	}
	logger, logCloser, err := app.NewLogger(cfg)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "initialize logger: %v\n", err)
		return 1
	}
	defer func() { _ = logCloser.Close() }()
	slog.SetDefault(logger)

	server, err := app.NewServer(context.Background(), cfg)
	if err != nil {
		logger.Error("initialize server", "error", err)
		return 1
	}

	errCh := make(chan error, 1)
	go func() { errCh <- server.Start() }()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-errCh:
		if err != nil {
			logger.Error("server stopped", "error", err)
			_ = server.Shutdown(context.Background())
			return 1
		}
	case <-stop:
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			logger.Error("server shutdown failed", "error", err)
			return 1
		}
	}
	return 0
}
