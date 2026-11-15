package runtimelsp

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
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
	sessions map[string]*managedSession
	mu       sync.Mutex
	closing  bool
	active   int
	// backgroundCloses counts the sessions that shut down in the background, so Close can wait for them.
	backgroundCloses sync.WaitGroup
	// forceBackgroundCloses closes when Close starts, so the background closes skip the graceful shutdown and kill their
	// servers at once.
	forceBackgroundCloses chan struct{}
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

	if config.SessionIdleTimeout < 0 {
		return nil, errors.New("session idle timeout must not be negative")
	}
	if config.SessionIdleTimeout == 0 {
		config.SessionIdleTimeout = defaultSessionIdleTimeout
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
		config:           config,
		sessions:         make(map[string]*managedSession),
		mu:               sync.Mutex{},
		closing:          false,
		active:           0,
		backgroundCloses: sync.WaitGroup{},

		forceBackgroundCloses: make(chan struct{}),
	}

	return runtime, nil
}

// EnsureConn starts the process on demand for one workspace root and returns its live JSON-RPC connection; when ctx
// carries the acquired session of this root, it uses only that session and fails after the session was closed.
func (r *Runtime) EnsureConn(ctx context.Context, workspaceRoot string) (jsonrpc2.Conn, error) {
	session, err := r.startedSession(ctx, workspaceRoot)
	if err != nil {
		return nil, err
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

// SessionInfo starts the process on demand for one workspace root and returns the captured initialize metadata; when
// ctx carries the acquired session of this root, it uses only that session and fails after the session was closed.
func (r *Runtime) SessionInfo(ctx context.Context, workspaceRoot string) (*SessionInfo, error) {
	session, err := r.startedSession(ctx, workspaceRoot)
	if err != nil {
		return nil, err
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

// startedSession returns the session of workspaceRoot with a started server: the acquired session that ctx carries
// for this root while it is still registered, or otherwise the session registered for the root.
func (r *Runtime) startedSession(ctx context.Context, workspaceRoot string) (*session, error) {
	root, err := normalizeWorkspaceRoot(workspaceRoot)
	if err != nil {
		return nil, err
	}

	if acquired := acquiredSessionFrom(ctx, r, root); acquired != nil {
		if !r.isRegistered(root, acquired.managed) {
			return nil, errSessionClosed
		}
		// A background close that starts after this check marks the session retired, so ensureStarted either fails or
		// starts a server that this close then stops.
		if startErr := acquired.managed.session.ensureStarted(ctx); startErr != nil {
			return nil, startErr
		}

		return acquired.managed.session, nil
	}

	release, acquireErr := r.AcquireSession(ctx, root)
	if acquireErr != nil {
		return nil, acquireErr
	}
	defer release()

	session, sessionErr := r.getOrCreateSession(root)
	if sessionErr != nil {
		return nil, sessionErr
	}
	if startErr := session.ensureStarted(ctx); startErr != nil {
		return nil, startErr
	}

	return session, nil
}

// isRegistered reports whether managed is the session registered for root.
func (r *Runtime) isRegistered(root string, managed *managedSession) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.sessions[root] == managed
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
		return existingSession.session, nil
	}

	config, err := r.newSessionConfig(normalizedWorkspaceRoot)
	if err != nil {
		return nil, err
	}

	createdSession := newSession(config)
	r.sessions[normalizedWorkspaceRoot] = &managedSession{
		session:      createdSession,
		requests:     0,
		idleTimer:    nil,
		idleDeadline: time.Time{},
		closing:      nil,
	}

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

// closeInBackground removes managed from the sessions of root and shuts it down in a goroutine that Close forces to
// stop and waits for; it does nothing when managed is no longer registered for root or the runtime is closing.
func (r *Runtime) closeInBackground(ctx context.Context, root string, managed *managedSession) {
	r.mu.Lock()
	if r.closing || r.sessions[root] != managed {
		r.mu.Unlock()

		return
	}
	managed.stopIdleTimer()
	delete(r.sessions, root)
	r.backgroundCloses.Add(1)
	forceStop := r.forceBackgroundCloses
	r.mu.Unlock()

	closeCtx := context.WithoutCancel(ctx)
	go func() {
		defer r.backgroundCloses.Done()

		// The session owns a bounded shutdown budget, so the close needs no deadline of the request.
		if closeErr := managed.session.retireUnlessForced(closeCtx, forceStop); closeErr != nil {
			slog.WarnContext(closeCtx, "close LSP session in background",
				"server_name", r.config.ServerName, "workspace_root", root, "err", closeErr)
		}
	}()
}

// Close shuts down all live sessions and the sessions that close in the background, so process-level cleanup stays
// explicit to the caller.
func (r *Runtime) Close(ctx context.Context) error {
	r.mu.Lock()
	if r.closing {
		r.mu.Unlock()
		return errRuntimeClosing
	}
	r.closing = true
	close(r.forceBackgroundCloses)
	r.mu.Unlock()

	// No background close starts while the runtime is closing; each running one kills its server at once and waits for
	// its exit up to ShutdownTimeout.
	r.backgroundCloses.Wait()

	closeCtx, cancel := newShutdownContext(ctx, r.config.ShutdownTimeout)
	defer cancel()

	if waitErr := r.waitForActiveRequests(closeCtx); waitErr != nil {
		r.endClosing()

		return waitErr
	}

	r.mu.Lock()
	sessions := make([]*session, 0, len(r.sessions))
	for _, session := range r.sessions {
		session.stopIdleTimer()
		sessions = append(sessions, session.session)
	}
	r.sessions = make(map[string]*managedSession)
	r.mu.Unlock()

	var closeErr error
	for _, session := range sessions {
		closeErr = errors.Join(closeErr, session.close(ctx))
	}

	r.endClosing()

	return closeErr
}

// endClosing lets the runtime serve requests again and gives later background closes a new force signal.
func (r *Runtime) endClosing() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.closing = false
	r.forceBackgroundCloses = make(chan struct{})
}

// isClosing reports whether runtime shutdown has started.
func (r *Runtime) isClosing() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.closing
}

// waitForActiveRequests waits for acquired sessions and idle cleanup to finish.
func (r *Runtime) waitForActiveRequests(ctx context.Context) error {
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
