// Package logging configures process-wide structured logging.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/n-r-w/asteria/internal/config"
	"gopkg.in/natefinch/lumberjack.v2"
)

// logDirPermission keeps log files private to the current user because logs can contain workspace paths and errors.
const logDirPermission = 0o700

// SetupStderrLogger installs the startup logger used before file logging configuration is loaded.
func SetupStderrLogger() {
	//nolint:exhaustruct // stdlib options use zero values for optional behavior.
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)
}

// SetupFileLogger installs the process logger that writes JSON records to stderr and the rotating log file.
func SetupFileLogger(cfg config.LogConfig) (func() error, error) {
	logDir := filepath.Dir(cfg.File)
	if err := os.MkdirAll(logDir, logDirPermission); err != nil {
		return nil, fmt.Errorf("create log directory %q: %w", logDir, err)
	}

	//nolint:exhaustruct // lumberjack optional fields keep documented package defaults.
	rotatingFile := &lumberjack.Logger{
		Filename:   cfg.File,
		MaxSize:    cfg.MaxSizeMB,
		MaxAge:     cfg.MaxAgeDays,
		MaxBackups: cfg.MaxBackups,
		Compress:   cfg.Compress,
	}
	writer := io.MultiWriter(os.Stderr, rotatingFile)

	//nolint:exhaustruct // stdlib options use zero values for optional behavior.
	logger := slog.New(slog.NewJSONHandler(writer, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	return rotatingFile.Close, nil
}
