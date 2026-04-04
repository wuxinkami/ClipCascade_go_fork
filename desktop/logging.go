package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/clipcascade/desktop/config"
)

func setupLogging(level slog.Level) (io.Closer, error) {
	logPath := config.LogFilePath()
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return nil, err
	}

	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}

	handler := slog.NewTextHandler(io.MultiWriter(os.Stdout, logFile), &slog.HandlerOptions{
		Level: level,
	})
	slog.SetDefault(slog.New(handler))
	return logFile, nil
}
