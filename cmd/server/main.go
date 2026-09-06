package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"chatgpt-space-merge/internal/httpapi"
	"chatgpt-space-merge/internal/store"
	"chatgpt-space-merge/internal/workflow"
)

func main() {
	addr := env("APP_ADDR", "127.0.0.1:18120")
	dataDir, err := filepath.Abs(env("APP_DATA_DIR", "./data"))
	if err != nil {
		fatal("解析数据目录失败", err)
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	dataStore, err := store.Open(dataDir)
	if err != nil {
		fatal("打开数据存储失败", err)
	}
	defer dataStore.Close()
	manager := workflow.NewManager(dataStore)
	api, err := httpapi.New(dataStore, manager)
	if err != nil {
		fatal("初始化 HTTP 服务失败", err)
	}
	server := &http.Server{Addr: addr, Handler: api.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 2 * time.Minute, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 1 << 20}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	api.StartBackground(ctx)
	go func() {
		<-ctx.Done()
		shutdown, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		_ = server.Shutdown(shutdown)
	}()
	slog.Info("chatgpt-space-merge listening", "addr", addr, "data_dir", dataDir)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fatal("HTTP 服务异常退出", err)
	}
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
func fatal(message string, err error) { slog.Error(message, "error", err); os.Exit(1) }
