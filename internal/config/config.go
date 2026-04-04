// Package config loads application configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	Adapters               cfgadapters.Config
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
