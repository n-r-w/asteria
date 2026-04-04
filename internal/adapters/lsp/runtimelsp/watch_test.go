package runtimelsp

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/fsnotify/fsnotify"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.lsp.dev/protocol"
)

// TestCollectWatchDirsSkipsIgnoredDirectories proves that runtime-managed watching stays inside the workspace
// while skipping adapter-declared noisy directories.
func TestCollectWatchDirsSkipsIgnoredDirectories(t *testing.T) {
	t.Parallel()

	workspaceRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspaceRoot, "src", "nested"), 0o750))
	require.NoError(t, os.MkdirAll(filepath.Join(workspaceRoot, "vendor", "pkg"), 0o750))
	require.NoError(t, os.MkdirAll(filepath.Join(workspaceRoot, ".cache"), 0o750))

	watchDirs, err := collectWatchDirs(workspaceRoot, func(relativePath string) bool {
		baseName := filepath.Base(relativePath)

		return baseName == "vendor" || baseName == ".cache"
	})
	require.NoError(t, err)

	relativeDirs := make([]string, 0, len(watchDirs))
	for _, watchDir := range watchDirs {
		relativePath, relErr := filepath.Rel(workspaceRoot, watchDir)
		require.NoError(t, relErr)
		relativeDirs = append(relativeDirs, filepath.Clean(relativePath))
	}
	sort.Strings(relativeDirs)

	assert.Equal(t, []string{".", filepath.Join("src"), filepath.Join("src", "nested")}, relativeDirs)
}

// TestAddWatchDirsSkipsMissingNestedDirectories proves that one transient nested directory cannot abort
// watcher startup for the whole workspace.
func TestAddWatchDirsSkipsMissingNestedDirectories(t *testing.T) {
	t.Parallel()

	workspaceRoot := filepath.Join(string(filepath.Separator), "workspace")
	missingDir := filepath.Join(workspaceRoot, "gone")
	trackedDir := filepath.Join(workspaceRoot, "src")
	called := make([]string, 0, 3)

	err := addWatchDirs(func(path string) error {
		called = append(called, path)
		if path == missingDir {
			return fs.ErrNotExist
		}

		return nil
	}, workspaceRoot, []string{workspaceRoot, missingDir, trackedDir})
	require.NoError(t, err)
	assert.Equal(t, []string{workspaceRoot, missingDir, trackedDir}, called)
}

// TestAddWatchDirsRejectsMissingWorkspaceRoot proves that startup still fails when the workspace root itself
// is gone, because that session cannot produce a coherent watch set anymore.
func TestAddWatchDirsRejectsMissingWorkspaceRoot(t *testing.T) {
	t.Parallel()

	workspaceRoot := filepath.Join(string(filepath.Separator), "workspace")

	err := addWatchDirs(func(string) error {
		return fs.ErrNotExist
	}, workspaceRoot, []string{workspaceRoot})
	require.ErrorIs(t, err, fs.ErrNotExist)
}

// TestTranslateWatchEventFiltersUnsupportedExtensions proves that runtime-managed notifications reach the LSP
// only for files the adapter explicitly marked as relevant.
func TestTranslateWatchEventFiltersUnsupportedExtensions(t *testing.T) {
	t.Parallel()

	workspaceRoot := t.TempDir()
	phpPath := filepath.Join(workspaceRoot, "fixture.php")
	txtPath := filepath.Join(workspaceRoot, "fixture.txt")
	require.NoError(t, os.WriteFile(phpPath, []byte("<?php\n"), 0o600))
	require.NoError(t, os.WriteFile(txtPath, []byte("text\n"), 0o600))

	relevantFile := func(relativePath string) bool {
		return filepath.Ext(relativePath) == ".php"
	}

	phpEffect, phpErr := translateWatchEvent(workspaceRoot, relevantFile, nil, fsnotify.Event{
		Name: phpPath,
		Op:   fsnotify.Write,
	})
	require.NoError(t, phpErr)
	require.Len(t, phpEffect.fileEvents, 1)
	assert.InDelta(
		t,
		float64(protocol.FileChangeTypeChanged),
		float64(phpEffect.fileEvents[0].Type),
		0,
	)

	txtEffect, txtErr := translateWatchEvent(workspaceRoot, relevantFile, nil, fsnotify.Event{
		Name: txtPath,
		Op:   fsnotify.Write,
	})
	require.NoError(t, txtErr)
	assert.Empty(t, txtEffect.fileEvents)
}

// TestTranslateWatchEventAddsNewDirectoryRecursively proves that newly created directories are added to the
// watch set immediately so later file changes under them do not fall off the radar.
func TestTranslateWatchEventAddsNewDirectoryRecursively(t *testing.T) {
	t.Parallel()

	workspaceRoot := t.TempDir()
	createdDir := filepath.Join(workspaceRoot, "generated")
	require.NoError(t, os.MkdirAll(filepath.Join(createdDir, "nested"), 0o750))

	effect, err := translateWatchEvent(workspaceRoot, func(string) bool { return true }, nil, fsnotify.Event{
		Name: createdDir,
		Op:   fsnotify.Create,
	})
	require.NoError(t, err)

	sort.Strings(effect.addDirs)
	assert.Equal(t, []string{createdDir, filepath.Join(createdDir, "nested")}, effect.addDirs)
	assert.Empty(t, effect.fileEvents)
}
