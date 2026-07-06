// Package config loads application configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/n-r-w/asteria/internal/config/cfgadapters"
	"github.com/samber/lo"
)

// Config holds static application configuration loaded from environment variables.
type Config struct {
	CacheRoot              string
	SystemPrompt           string
	GetSymbolsOverviewDesc string
	FindSymbolDesc         string
	FindReferencesDesc     string
	ToolTimeout            time.Duration
	ToolOutputMaxBytes     int
	Log                    LogConfig
	Adapters               cfgadapters.Config
}

// LogConfig controls file logging and rotation limits for the process logger.
type LogConfig struct {
	// File is the current JSON log file path. Rotated files stay in the same directory.
	File string
	// MaxSizeMB is the active log file size limit before rotation.
	MaxSizeMB int
	// MaxAgeDays is the retention horizon for rotated log files.
	MaxAgeDays int
	// MaxBackups limits how many rotated log files can remain on disk.
	MaxBackups int
	// Compress stores rotated files as gzip archives to reduce disk usage.
	Compress bool
}

// envConfig is an intermediate struct for parsing environment variables.
type envConfig struct {
	CacheRoot              string        `env:"ASTERIAMCP_CACHE_ROOT"`
	SystemPrompt           string        `env:"ASTERIAMCP_SYSTEM_PROMPT"`
	GetSymbolsOverviewDesc string        `env:"ASTERIAMCP_GET_SYMBOLS_OVERVIEW_DESC"`
	FindSymbolDesc         string        `env:"ASTERIAMCP_FIND_SYMBOL_DESC"`
	FindReferencesDesc     string        `env:"ASTERIAMCP_FIND_REFERENCES_DESC"`
	ToolTimeout            time.Duration `env:"ASTERIAMCP_TOOL_TIMEOUT" envDefault:"360s"`
	ToolOutputMaxBytes     int           `env:"ASTERIAMCP_TOOL_OUTPUT_MAX_BYTES" envDefault:"32768"` // ~ 8K tokens
	LogFile                string        `env:"ASTERIAMCP_LOG_FILE"`
	LogMaxSizeMB           int           `env:"ASTERIAMCP_LOG_MAX_SIZE_MB" envDefault:"20"`
	LogMaxAgeDays          int           `env:"ASTERIAMCP_LOG_MAX_AGE_DAYS" envDefault:"14"`
	LogMaxBackups          int           `env:"ASTERIAMCP_LOG_MAX_BACKUPS" envDefault:"20"`
	LogCompress            bool          `env:"ASTERIAMCP_LOG_COMPRESS" envDefault:"true"`
	XDGStateHome           string        `env:"XDG_STATE_HOME"`
	LocalAppData           string        `env:"LOCALAPPDATA"`
	cfgadapters.EnvConfig
}

// Load parses configuration from environment variables and validates it.
// It should be called once at application startup.
func Load() (*Config, error) {
	var ec envConfig
	if err := env.Parse(&ec); err != nil {
		return nil, fmt.Errorf("parse env config: %w", err)
	}

	return buildConfig(&ec)
}

// buildConfig normalizes already-parsed env values and validates the final static application config.
func buildConfig(ec *envConfig) (*Config, error) {
	ec.CacheRoot = strings.TrimSpace(ec.CacheRoot)
	ec.SystemPrompt = strings.TrimSpace(ec.SystemPrompt)
	ec.GetSymbolsOverviewDesc = strings.TrimSpace(ec.GetSymbolsOverviewDesc)
	ec.FindSymbolDesc = strings.TrimSpace(ec.FindSymbolDesc)
	ec.FindReferencesDesc = strings.TrimSpace(ec.FindReferencesDesc)
	ec.LogFile = strings.TrimSpace(ec.LogFile)
	ec.XDGStateHome = strings.TrimSpace(ec.XDGStateHome)
	ec.LocalAppData = strings.TrimSpace(ec.LocalAppData)
	cacheRoot, err := defaultCacheRoot()
	if err != nil {
		return nil, err
	}
	if ec.CacheRoot != "" {
		if !filepath.IsAbs(ec.CacheRoot) {
			return nil, errors.New("ASTERIAMCP_CACHE_ROOT must be absolute")
		}

		cacheRoot = filepath.Clean(ec.CacheRoot)
	}
	if ec.ToolOutputMaxBytes <= 0 {
		return nil, errors.New("ASTERIAMCP_TOOL_OUTPUT_MAX_BYTES must be positive")
	}
	if ec.ToolTimeout <= 0 {
		return nil, errors.New("ASTERIAMCP_TOOL_TIMEOUT must be positive")
	}
	logConfig, err := buildLogConfig(ec)
	if err != nil {
		return nil, err
	}

	adapterConfig, err := ec.Build()
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		CacheRoot:    cacheRoot,
		SystemPrompt: lo.Ternary(ec.SystemPrompt != "", ec.SystemPrompt, systemPrompt),
		GetSymbolsOverviewDesc: lo.Ternary(ec.GetSymbolsOverviewDesc != "",
			ec.GetSymbolsOverviewDesc, toolGetSymbolsOverviewDesc),
		FindSymbolDesc:     lo.Ternary(ec.FindSymbolDesc != "", ec.FindSymbolDesc, toolFindSymbolDesc),
		FindReferencesDesc: lo.Ternary(ec.FindReferencesDesc != "", ec.FindReferencesDesc, toolFindReferencesDesc),
		ToolTimeout:        ec.ToolTimeout,
		ToolOutputMaxBytes: ec.ToolOutputMaxBytes,
		Log:                logConfig,
		Adapters:           adapterConfig,
	}

	return cfg, nil
}

// defaultCacheRoot keeps managed adapter artifacts under the OS user cache
// directory when callers do not override the base path.
func defaultCacheRoot() (string, error) {
	userCacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("resolve user cache dir: %w", err)
	}

	return filepath.Join(userCacheDir, "asteria", "cache"), nil
}

// buildLogConfig validates file logging limits and resolves the default log file path when needed.
func buildLogConfig(ec *envConfig) (LogConfig, error) {
	logFile, err := defaultLogFile(ec)
	if err != nil {
		return LogConfig{}, err
	}
	if ec.LogFile != "" {
		if !filepath.IsAbs(ec.LogFile) {
			return LogConfig{}, errors.New("ASTERIAMCP_LOG_FILE must be absolute")
		}

		logFile = filepath.Clean(ec.LogFile)
	}
	if ec.LogMaxSizeMB <= 0 {
		return LogConfig{}, errors.New("ASTERIAMCP_LOG_MAX_SIZE_MB must be positive")
	}
	if ec.LogMaxAgeDays <= 0 {
		return LogConfig{}, errors.New("ASTERIAMCP_LOG_MAX_AGE_DAYS must be positive")
	}
	if ec.LogMaxBackups <= 0 {
		return LogConfig{}, errors.New("ASTERIAMCP_LOG_MAX_BACKUPS must be positive")
	}

	return LogConfig{
		File:       logFile,
		MaxSizeMB:  ec.LogMaxSizeMB,
		MaxAgeDays: ec.LogMaxAgeDays,
		MaxBackups: ec.LogMaxBackups,
		Compress:   ec.LogCompress,
	}, nil
}

// defaultLogFile returns the standard per-user log path for the current operating system.
func defaultLogFile(ec *envConfig) (string, error) {
	switch runtime.GOOS {
	case "darwin":
		return defaultDarwinLogFile()
	case "windows":
		return defaultWindowsLogFile(ec.LocalAppData)
	default:
		return defaultXDGLogFile(ec.XDGStateHome)
	}
}

// defaultDarwinLogFile follows macOS user-domain convention for application logs.
func defaultDarwinLogFile() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home dir for default log file: %w", err)
	}

	return filepath.Join(homeDir, "Library", "Logs", "asteria", "asteria.log"), nil
}

// defaultWindowsLogFile follows the Windows local app data convention for per-user logs.
func defaultWindowsLogFile(localAppData string) (string, error) {
	if localAppData == "" {
		return "", errors.New("LOCALAPPDATA is required to build default ASTERIAMCP_LOG_FILE on Windows")
	}
	if !filepath.IsAbs(localAppData) {
		return "", errors.New("LOCALAPPDATA must be absolute")
	}

	return filepath.Join(filepath.Clean(localAppData), "asteria", "logs", "asteria.log"), nil
}

// defaultXDGLogFile follows XDG_STATE_HOME because XDG state data explicitly includes logs.
func defaultXDGLogFile(xdgStateHome string) (string, error) {
	stateHome := xdgStateHome
	if stateHome == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve user home dir for default log file: %w", err)
		}

		stateHome = filepath.Join(homeDir, ".local", "state")
	}
	if !filepath.IsAbs(stateHome) {
		return "", errors.New("XDG_STATE_HOME must be absolute")
	}

	return filepath.Join(filepath.Clean(stateHome), "asteria", "logs", "asteria.log"), nil
}
