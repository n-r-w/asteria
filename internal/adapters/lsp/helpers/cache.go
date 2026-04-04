// Package helpers keeps adapter helpers shared across packages.
package helpers

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

const adaptersCacheDirName = "adapters"

// ResolveCacheRoot validates one configured cache root and normalizes it without depending on process cwd.
func ResolveCacheRoot(cacheRoot string) (string, error) {
	trimmedCacheRoot := strings.TrimSpace(cacheRoot)
	if trimmedCacheRoot == "" {
		return "", errors.New("cache root is required")
	}
	if !filepath.IsAbs(trimmedCacheRoot) {
		return "", fmt.Errorf("cache root %q must be absolute", trimmedCacheRoot)
	}

	return filepath.Clean(trimmedCacheRoot), nil
}

// WorkspaceHash returns the stable cache namespace for one normalized workspace root.
func WorkspaceHash(normalizedWorkspaceRoot string) string {
	hash := sha256.Sum256([]byte(normalizedWorkspaceRoot))

	return hex.EncodeToString(hash[:])
}

// AdapterCacheDir returns the managed cache directory for one adapter under one normalized workspace root.
func AdapterCacheDir(cacheRoot, workspaceRoot, adapterName string) (string, error) {
	normalizedCacheRoot, err := ResolveCacheRoot(cacheRoot)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(adapterName) == "" {
		return "", errors.New("adapter name is required")
	}

	normalizedWorkspaceRoot, err := ResolveWorkspaceRoot(workspaceRoot)
	if err != nil {
		return "", err
	}

	return filepath.Join(
		normalizedCacheRoot,
		WorkspaceHash(normalizedWorkspaceRoot),
		adaptersCacheDirName,
		adapterName,
	), nil
}
