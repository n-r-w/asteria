package runtimelsp

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.lsp.dev/jsonrpc2"
)

const runtimeClosePollInterval = 10 * time.Millisecond

// Runtime owns one reusable LSP session per normalized workspace root and exposes live JSON-RPC connections.
type Runtime struct {
	config   *RuntimeConfig
	sessions map[string]*session
	mu       sync.Mutex
	closing  bool
	active   int
}

// New validates runtime configuration, normalizes defaults in place, and prepares lazy session storage.
func New(config *RuntimeConfig) (*Runtime, error) {
	if config == nil {
		return nil, errors.New("runtime config is nil")
	}

	var err error
	if config.Command == "" {
		err = errors.Join(err, errors.New("command is required"))
	}
	if config.ServerName == "" {
		err = errors.Join(err, errors.New("server name is required"))
	}
	if err != nil {
		return nil, fmt.Errorf("invalid runtime config: %w", err)
	}

	if config.ShutdownTimeout <= 0 {
		config.ShutdownTimeout = defaultShutdownTimeout
	}
	if config.BuildClientCapabilities == nil {
		config.BuildClientCapabilities = runtimeClientCapabilities
	}
	if config.BuildWorkspaceFolders == nil {
		config.BuildWorkspaceFolders = defaultWorkspaceFolders
	}

	runtime := &Runtime{
		config:   config,
		sessions: make(map[string]*session),
		mu:       sync.Mutex{},
		closing:  false,
		active:   0,
	}

	return runtime, nil
}

// EnsureConn starts the process on demand for one workspace root and returns its live JSON-RPC connection.
func (r *Runtime) EnsureConn(ctx context.Context, workspaceRoot string) (jsonrpc2.Conn, error) {
	normalizedWorkspaceRoot, err := normalizeWorkspaceRoot(workspaceRoot)
	if err != nil {
		return nil, err
	}
	if beginErr := r.beginEnsureConn(); beginErr != nil {
		return nil, beginErr
	}
	defer r.endEnsureConn()

	session, sessionErr := r.getOrCreateSession(normalizedWorkspaceRoot)
	if sessionErr != nil {
		return nil, sessionErr
	}
	if startErr := session.ensureStarted(ctx); startErr != nil {
		return nil, startErr
	}
	if r.isClosing() {
		return nil, errRuntimeClosing
	}

	conn := session.connection()
	if conn == nil {
		return nil, errors.New("runtime connection is not available")
	}

	return conn, nil
}

// SessionInfo starts the process on demand for one workspace root and returns the captured initialize metadata.
func (r *Runtime) SessionInfo(ctx context.Context, workspaceRoot string) (*SessionInfo, error) {
	normalizedWorkspaceRoot, err := normalizeWorkspaceRoot(workspaceRoot)
	if err != nil {
		return nil, err
	}
	if beginErr := r.beginEnsureConn(); beginErr != nil {
		return nil, beginErr
	}
	defer r.endEnsureConn()

	session, sessionErr := r.getOrCreateSession(normalizedWorkspaceRoot)
	if sessionErr != nil {
		return nil, sessionErr
	}
	if startErr := session.ensureStarted(ctx); startErr != nil {
		return nil, startErr
	}
	if r.isClosing() {
		return nil, errRuntimeClosing
	}

	info := session.infoSnapshot()
	if info == nil || info.InitializeResult == nil {
		return nil, errors.New("runtime session info is not available")
	}

	return info, nil
}

// getOrCreateSession resolves one normalized root and reuses or creates its session wrapper.
func (r *Runtime) getOrCreateSession(workspaceRoot string) (*session, error) {
	normalizedWorkspaceRoot, err := normalizeWorkspaceRoot(workspaceRoot)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	return r.getOrCreateSessionLocked(normalizedWorkspaceRoot)
}

// getOrCreateSessionLocked reuses or creates one session while the runtime mutex is held.
func (r *Runtime) getOrCreateSessionLocked(normalizedWorkspaceRoot string) (*session, error) {
	if existingSession, ok := r.sessions[normalizedWorkspaceRoot]; ok {
		return existingSession, nil
	}

	config, err := r.newSessionConfig(normalizedWorkspaceRoot)
	if err != nil {
		return nil, err
	}

	createdSession := newSession(config)
	r.sessions[normalizedWorkspaceRoot] = createdSession

	return createdSession, nil
}

// newSessionConfig materializes one root-bound session configuration from the runtime baseline.
func (r *Runtime) newSessionConfig(workspaceRoot string) (*sessionConfig, error) {
	if r.config == nil {
		return nil, errors.New("runtime config is nil")
	}

	workspaceFolders := r.config.BuildWorkspaceFolders(workspaceRoot)
	if len(workspaceFolders) == 0 {
		workspaceFolders = defaultWorkspaceFolders(workspaceRoot)
	}

	return &sessionConfig{
		LSPConfig:        r.config.LSPConfig,
		WorkspaceRoot:    workspaceRoot,
		WorkspaceFolders: workspaceFolders,
	}, nil
}

// Close shuts down all live sessions so process-level cleanup stays explicit to the caller.
func (r *Runtime) Close(ctx context.Context) error {
	r.mu.Lock()
	if r.closing {
		r.mu.Unlock()
		return errRuntimeClosing
	}
	r.closing = true
	r.mu.Unlock()

	if waitErr := r.waitForActiveEnsureConn(ctx); waitErr != nil {
		r.mu.Lock()
		r.closing = false
		r.mu.Unlock()

		return waitErr
	}

	r.mu.Lock()
	sessions := make([]*session, 0, len(r.sessions))
	for _, session := range r.sessions {
		sessions = append(sessions, session)
	}
	r.sessions = make(map[string]*session)
	r.mu.Unlock()

	var closeErr error
	for _, session := range sessions {
		closeErr = errors.Join(closeErr, session.close(ctx))
	}

	r.mu.Lock()
	r.closing = false
	r.mu.Unlock()

	return closeErr
}

// beginEnsureConn marks one in-flight EnsureConn call unless the runtime is already closing.
func (r *Runtime) beginEnsureConn() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closing {
		return errRuntimeClosing
	}

	r.active++

	return nil
}

// endEnsureConn releases one in-flight EnsureConn call and wakes Close when the runtime becomes idle.
func (r *Runtime) endEnsureConn() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.active--
}

// isClosing reports whether runtime shutdown has started.
func (r *Runtime) isClosing() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.closing
}

// waitForActiveEnsureConn waits until no EnsureConn calls remain active or the caller cancels shutdown.
func (r *Runtime) waitForActiveEnsureConn(ctx context.Context) error {
	ticker := time.NewTicker(runtimeClosePollInterval)
	defer ticker.Stop()

	for {
		r.mu.Lock()
		active := r.active
		r.mu.Unlock()
		if active == 0 {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// normalizeWorkspaceRoot validates and canonicalizes one runtime session key.
func normalizeWorkspaceRoot(workspaceRoot string) (string, error) {
	trimmedWorkspaceRoot := strings.TrimSpace(workspaceRoot)
	if trimmedWorkspaceRoot == "" {
		return "", errors.New("workspace root is required")
	}
	if !filepath.IsAbs(trimmedWorkspaceRoot) {
		return "", errors.New("workspace root must be absolute")
	}

	cleanWorkspaceRoot := filepath.Clean(trimmedWorkspaceRoot)
	normalizedWorkspaceRoot, err := filepath.EvalSymlinks(cleanWorkspaceRoot)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("workspace root %q not found: %w", cleanWorkspaceRoot, err)
		}

		return "", fmt.Errorf("normalize workspace root %q: %w", cleanWorkspaceRoot, err)
	}

	fileInfo, err := os.Stat(normalizedWorkspaceRoot)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("workspace root %q not found: %w", cleanWorkspaceRoot, err)
		}

		return "", fmt.Errorf("stat workspace root %q: %w", cleanWorkspaceRoot, err)
	}
	if !fileInfo.IsDir() {
		return "", fmt.Errorf("workspace root %q must point to a directory", cleanWorkspaceRoot)
	}

	return normalizedWorkspaceRoot, nil
}
