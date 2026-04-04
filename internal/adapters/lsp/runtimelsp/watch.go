package runtimelsp

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fsnotify/fsnotify"
	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

// watchEffect describes how one filesystem event changes the runtime-managed watch state and the LSP view.
type watchEffect struct {
	fileEvents []*protocol.FileEvent
	addDirs    []string
}

// workspaceFileWatcher keeps one workspace tree mirrored into LSP watched-files notifications.
type workspaceFileWatcher struct {
	workspaceRoot string
	relevantFile  func(string) bool
	ignoreDir     func(string) bool
	conn          jsonrpc2.Conn
	watcher       *fsnotify.Watcher
	done          chan struct{}
	closeOnce     sync.Once
}

// newWorkspaceFileWatcher starts recursive directory watching for one workspace and forwards relevant
// filesystem changes to the connected language server.
func newWorkspaceFileWatcher(
	workspaceRoot string,
	conn jsonrpc2.Conn,
	config *FileWatchConfig,
) (*workspaceFileWatcher, error) {
	if config == nil || config.RelevantFile == nil {
		return nil, errors.New("file watch config requires RelevantFile")
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	fileWatcher := &workspaceFileWatcher{
		workspaceRoot: workspaceRoot,
		relevantFile:  config.RelevantFile,
		ignoreDir:     config.IgnoreDir,
		conn:          conn,
		watcher:       watcher,
		done:          make(chan struct{}),
		closeOnce:     sync.Once{},
	}

	watchDirs, err := collectWatchDirs(workspaceRoot, config.IgnoreDir)
	if err != nil {
		_ = watcher.Close()
		return nil, err
	}
	if err = addWatchDirs(watcher.Add, workspaceRoot, watchDirs); err != nil {
		_ = watcher.Close()
		return nil, err
	}

	go fileWatcher.run()

	return fileWatcher, nil
}

// close stops the underlying fsnotify watcher and waits until the forwarding goroutine is gone.
func (w *workspaceFileWatcher) close() error {
	if w == nil {
		return nil
	}

	var closeErr error
	w.closeOnce.Do(func() {
		closeErr = w.watcher.Close()
		<-w.done
	})

	return closeErr
}

// run forwards fsnotify events until the watcher is closed.
func (w *workspaceFileWatcher) run() {
	defer close(w.done)

	for {
		select {
		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			w.handleEvent(event)
		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			if err != nil {
				slog.Warn("workspace file watcher error", "workspace_root", w.workspaceRoot, "error", err)
			}
		}
	}
}

// handleEvent applies one filesystem change to the recursive watch set and emits any matching LSP notification.
func (w *workspaceFileWatcher) handleEvent(event fsnotify.Event) {
	effect, err := translateWatchEvent(w.workspaceRoot, w.relevantFile, w.ignoreDir, event)
	if err != nil {
		slog.Warn("skip workspace file watcher event", "workspace_root", w.workspaceRoot, "path", event.Name, "error", err)
		return
	}

	for _, watchDir := range effect.addDirs {
		if addErr := w.watcher.Add(watchDir); addErr != nil &&
			!shouldIgnoreWatchDirError(w.workspaceRoot, watchDir, addErr) {
			slog.Warn(
				"skip workspace file watcher directory",
				"workspace_root", w.workspaceRoot,
				"path", watchDir,
				"error", addErr,
			)
		}
	}
	if len(effect.fileEvents) == 0 {
		return
	}

	params := &protocol.DidChangeWatchedFilesParams{Changes: effect.fileEvents}
	if notifyErr := w.conn.Notify(
		context.Background(),
		protocol.MethodWorkspaceDidChangeWatchedFiles,
		params,
	); notifyErr != nil {
		slog.Warn("workspace file watcher notify failed", "workspace_root", w.workspaceRoot, "error", notifyErr)
	}
}

// collectWatchDirs walks one workspace tree and returns every directory that must be subscribed recursively.
func collectWatchDirs(workspaceRoot string, ignoreDir func(string) bool) ([]string, error) {
	watchDirs := make([]string, 0)
	err := filepath.WalkDir(workspaceRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if shouldIgnoreWatchDirError(workspaceRoot, path, walkErr) {
				return nil
			}

			return walkErr
		}
		if !entry.IsDir() {
			return nil
		}

		relativePath, err := filepath.Rel(workspaceRoot, path)
		if err != nil {
			return err
		}
		relativePath = filepath.Clean(relativePath)
		if relativePath != "." && ignoreDir != nil && ignoreDir(relativePath) {
			return filepath.SkipDir
		}

		watchDirs = append(watchDirs, path)
		return nil
	})
	if err != nil {
		return nil, err
	}

	return watchDirs, nil
}

// addWatchDirs registers one batch of directories and tolerates nested paths that disappeared during startup.
func addWatchDirs(watchDirAdder func(string) error, workspaceRoot string, watchDirs []string) error {
	for _, watchDir := range watchDirs {
		if err := watchDirAdder(watchDir); err != nil {
			if shouldIgnoreWatchDirError(workspaceRoot, watchDir, err) {
				continue
			}

			return err
		}
	}

	return nil
}

// shouldIgnoreWatchDirError keeps transient nested-directory churn from aborting one whole LSP session startup.
func shouldIgnoreWatchDirError(workspaceRoot, path string, err error) bool {
	if !errors.Is(err, fs.ErrNotExist) {
		return false
	}

	return filepath.Clean(path) != filepath.Clean(workspaceRoot)
}

// translateWatchEvent maps one fsnotify event into recursive watch updates and filtered LSP file notifications.
func translateWatchEvent(
	workspaceRoot string,
	relevantFile func(string) bool,
	ignoreDir func(string) bool,
	event fsnotify.Event,
) (watchEffect, error) {
	absolutePath := filepath.Clean(event.Name)
	relativePath, err := filepath.Rel(workspaceRoot, absolutePath)
	if err != nil {
		return watchEffect{}, err
	}
	if relativePath == ".." || strings.HasPrefix(relativePath, ".."+string(os.PathSeparator)) {
		return watchEffect{}, nil
	}
	relativePath = filepath.Clean(relativePath)

	effect := watchEffect{
		fileEvents: nil,
		addDirs:    nil,
	}

	if event.Op&fsnotify.Create != 0 {
		createdDirs, collectErr := collectCreatedWatchDirs(workspaceRoot, absolutePath, ignoreDir)
		if collectErr != nil {
			return watchEffect{}, collectErr
		}
		effect.addDirs = createdDirs
	}
	if len(effect.addDirs) > 0 {
		return effect, nil
	}

	if relevantFile == nil || !relevantFile(relativePath) {
		return effect, nil
	}

	changeType, ok := mapFileChangeType(event.Op)
	if !ok {
		return effect, nil
	}
	effect.fileEvents = []*protocol.FileEvent{{
		Type: changeType,
		URI:  uri.File(absolutePath),
	}}

	return effect, nil
}

// collectCreatedWatchDirs returns newly created directories that must be subscribed recursively.
func collectCreatedWatchDirs(workspaceRoot, absolutePath string, ignoreDir func(string) bool) ([]string, error) {
	fileInfo, err := os.Stat(absolutePath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}

		return nil, err
	}
	if !fileInfo.IsDir() {
		return nil, nil
	}
	if ignoreDir != nil {
		relativeToWorkspace, relErr := filepath.Rel(workspaceRoot, absolutePath)
		if relErr != nil {
			return nil, relErr
		}
		if ignoreDir(filepath.Clean(relativeToWorkspace)) {
			return nil, nil
		}
	}

	return collectWatchDirs(absolutePath, func(relativePath string) bool {
		if ignoreDir == nil {
			return false
		}
		joinedPath := relativePath
		if joinedPath == "." {
			relativeToWorkspace, relErr := filepath.Rel(workspaceRoot, absolutePath)
			if relErr != nil {
				return false
			}
			joinedPath = filepath.Clean(relativeToWorkspace)
		} else {
			relativeToWorkspace, relErr := filepath.Rel(workspaceRoot, filepath.Join(absolutePath, relativePath))
			if relErr != nil {
				return false
			}
			joinedPath = filepath.Clean(relativeToWorkspace)
		}

		return ignoreDir(joinedPath)
	})
}

// mapFileChangeType translates fsnotify operations into the closest LSP watched-files event type.
func mapFileChangeType(op fsnotify.Op) (protocol.FileChangeType, bool) {
	switch {
	case op&fsnotify.Create != 0:
		return protocol.FileChangeTypeCreated, true
	case op&(fsnotify.Remove|fsnotify.Rename) != 0:
		return protocol.FileChangeTypeDeleted, true
	case op&(fsnotify.Write|fsnotify.Chmod) != 0:
		return protocol.FileChangeTypeChanged, true
	default:
		return 0, false
	}
}
