package runtimelsp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"

	"github.com/n-r-w/asteria/internal/domain"
	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
)

// session stores the live language server process and the JSON-RPC connection bound to it.
type session struct {
	config *sessionConfig
	mu     sync.Mutex

	cmd         *exec.Cmd
	conn        jsonrpc2.Conn
	fileWatcher *workspaceFileWatcher
	info        *SessionInfo

	done    chan struct{}
	waitErr error
}

// newSession creates a reusable session holder before any server process is started.
func newSession(config *sessionConfig) *session {
	return &session{
		config:      config,
		mu:          sync.Mutex{},
		cmd:         nil,
		conn:        nil,
		fileWatcher: nil,
		info:        nil,
		done:        nil,
		waitErr:     nil,
	}
}

// connection centralizes the nil-before-start invariant so callers do not inspect session state directly.
func (s *session) connection() jsonrpc2.Conn {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.conn
}

// infoSnapshot returns one shallow read-only copy of the session metadata captured after initialize.
func (s *session) infoSnapshot() *SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.info == nil || s.info.InitializeResult == nil {
		return nil
	}

	initializeResultCopy := *s.info.InitializeResult
	if s.info.InitializeResult.ServerInfo != nil {
		serverInfoCopy := *s.info.InitializeResult.ServerInfo
		initializeResultCopy.ServerInfo = &serverInfoCopy
	}

	return &SessionInfo{InitializeResult: &initializeResultCopy}
}

// ensureStarted makes sure the session has a live language server process before it is used.
func (s *session) ensureStarted(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.isAliveLocked() {
		return nil
	}

	if s.cmd != nil || s.conn != nil || s.done != nil {
		if err := s.closeLocked(ctx); err != nil {
			return err
		}
	}

	return s.startLocked(ctx)
}

// isAliveLocked reports whether the language server process is still available while the session lock is held.
func (s *session) isAliveLocked() bool {
	if s.done == nil || s.cmd == nil || s.conn == nil {
		return false
	}

	select {
	case <-s.done:
		return false
	default:
		return true
	}
}

// startLocked launches the configured server, binds JSON-RPC over stdio, and completes the LSP handshake.
func (s *session) startLocked(ctx context.Context) error {
	startupCtx := context.WithoutCancel(ctx)

	processID, err := currentProcessID()
	if err != nil {
		return err
	}

	var stderr bytes.Buffer
	//nolint:gosec // RuntimeConfig is built in code for known language servers, not from user input.
	cmd := exec.CommandContext(startupCtx, s.config.Command, s.config.Args...)
	cmd.Dir = s.config.WorkspaceRoot
	cmd.Stderr = &stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("open %s stdin: %w", s.config.ServerName, err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("open %s stdout: %w", s.config.ServerName, err)
	}

	if err = cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", s.config.ServerName, err)
	}

	// Persist the process state before the handshake so cleanup can unwind partial startup.
	s.cmd = cmd
	s.conn = jsonrpc2.NewConn(jsonrpc2.NewStream(&stdioConn{reader: stdout, writer: stdin}))
	s.done = make(chan struct{})
	s.waitErr = nil

	go func() {
		s.waitErr = cmd.Wait()
		close(s.done)
	}()

	clientHandler := newClientHandler(
		s.config.WorkspaceRoot,
		s.config.WorkspaceFolders,
		s.config.ReplyConfiguration,
		s.config.HandleServerCallback,
	)
	s.conn.Go(startupCtx, clientHandler)

	initializeParams, err := s.initializeParams(processID)
	if err != nil {
		shutdownErr := s.closeLocked(startupCtx)
		return errors.Join(wrapSessionError("prepare "+s.config.ServerName+" initialize params", err, &stderr), shutdownErr)
	}

	var initializeResult *protocol.InitializeResult
	if err = protocol.Call(ctx, s.conn, protocol.MethodInitialize, initializeParams, &initializeResult); err != nil {
		shutdownErr := s.closeLocked(startupCtx)
		return errors.Join(wrapSessionError("initialize "+s.config.ServerName, err, &stderr), shutdownErr)
	}
	s.info = &SessionInfo{InitializeResult: initializeResult}

	if err = s.conn.Notify(ctx, protocol.MethodInitialized, &protocol.InitializedParams{}); err != nil {
		shutdownErr := s.closeLocked(startupCtx)
		return errors.Join(
			wrapSessionError("notify "+s.config.ServerName+" initialized", err, &stderr),
			shutdownErr,
		)
	}

	if s.config.AfterInitialized != nil {
		if err = s.config.AfterInitialized(ctx, s.conn, s.config.WorkspaceRoot); err != nil {
			shutdownErr := s.closeLocked(startupCtx)
			return errors.Join(
				wrapSessionError("post-initialize "+s.config.ServerName, err, &stderr),
				shutdownErr,
			)
		}
	}

	if s.config.WaitUntilReady != nil {
		if err = s.config.WaitUntilReady(ctx, s.conn, s.config.WorkspaceRoot); err != nil {
			shutdownErr := s.closeLocked(startupCtx)
			return errors.Join(
				wrapSessionError("await "+s.config.ServerName+" ready", err, &stderr),
				shutdownErr,
			)
		}
	}

	if s.config.FileWatch != nil && s.config.FileWatch.RelevantFile != nil {
		fileWatcher, watchErr := newWorkspaceFileWatcher(s.config.WorkspaceRoot, s.conn, s.config.FileWatch)
		if watchErr != nil {
			shutdownErr := s.closeLocked(startupCtx)
			return errors.Join(
				wrapSessionError("start "+s.config.ServerName+" file watcher", watchErr, &stderr),
				shutdownErr,
			)
		}
		s.fileWatcher = fileWatcher
	}

	return nil
}

// initializeParams keeps the default LSP handshake in one place so tests can verify the runtime contract directly.
func (s *session) initializeParams(processID int32) (*protocol.InitializeParams, error) {
	// WorkspaceFolders is the supported replacement for the deprecated root fields.
	//nolint:exhaustruct // The literal intentionally omits deprecated RootPath and RootURI.
	params := &protocol.InitializeParams{
		WorkDoneProgressParams: protocol.WorkDoneProgressParams{},
		ProcessID:              processID,
		ClientInfo: &protocol.ClientInfo{
			Name:    domain.ServerName,
			Version: "",
		},
		Locale:                "en",
		InitializationOptions: nil,
		Capabilities:          s.config.BuildClientCapabilities(),
		Trace:                 protocol.TraceOff,
		WorkspaceFolders:      s.config.WorkspaceFolders,
	}
	if s.config.FileWatch != nil && s.config.FileWatch.RelevantFile != nil {
		params.Capabilities = enableDidChangeWatchedFilesCapability(params.Capabilities)
	}

	if s.config.PatchInitializeParams != nil {
		if err := s.config.PatchInitializeParams(s.config.WorkspaceRoot, params); err != nil {
			return nil, err
		}
	}

	return params, nil
}

// close gracefully shuts down the language server before the process pipes are torn down.
func (s *session) close(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.closeLocked(ctx)
}

// closeLocked gracefully shuts down the language server while the session lock is held.
func (s *session) closeLocked(ctx context.Context) error {
	if s.cmd == nil && s.conn == nil && s.fileWatcher == nil && s.done == nil {
		return nil
	}

	var closeErr error
	if s.fileWatcher != nil {
		closeErr = errors.Join(
			closeErr,
			wrapShutdownError("close "+s.config.ServerName+" file watcher", s.fileWatcher.close()),
		)
		s.fileWatcher = nil
	}

	var (
		baseCtx  = context.WithoutCancel(ctx)
		closeCtx context.Context
		cancel   context.CancelFunc
	)

	// context.WithoutCancel strips deadlines too, so we reapply the caller deadline when it exists.
	if deadline, hasDeadline := ctx.Deadline(); hasDeadline {
		closeCtx, cancel = context.WithDeadline(baseCtx, deadline)
	} else {
		closeCtx, cancel = context.WithTimeout(baseCtx, s.config.ShutdownTimeout)
	}
	defer cancel()

	if s.conn != nil {
		shutdownErr := protocol.Call(closeCtx, s.conn, protocol.MethodShutdown, nil, nil)
		closeErr = errors.Join(closeErr, wrapShutdownError("shutdown "+s.config.ServerName, shutdownErr))
		exitErr := s.conn.Notify(closeCtx, protocol.MethodExit, nil)
		closeErr = errors.Join(closeErr, wrapShutdownError("exit "+s.config.ServerName, exitErr))
	}

	if s.done == nil {
		if s.conn != nil {
			closeErr = errors.Join(
				closeErr,
				wrapShutdownError("close "+s.config.ServerName+" connection", normalizeConnCloseError(s.conn.Close())),
			)
		}
		s.resetLocked()
		return closeErr
	}

	select {
	case <-s.done:
		closeErr = errors.Join(
			closeErr,
			wrapShutdownError("wait for "+s.config.ServerName, normalizeWaitError(s.waitErr)),
		)
	case <-closeCtx.Done():
		killErr := wrapShutdownError("kill "+s.config.ServerName, killProcess(s.cmd))
		closeErr = errors.Join(closeErr, killErr, closeCtx.Err())
		<-s.done
		closeErr = errors.Join(
			closeErr,
			wrapShutdownError("wait for "+s.config.ServerName, normalizeWaitError(s.waitErr)),
		)
	}

	if s.conn != nil {
		closeErr = errors.Join(
			closeErr,
			wrapShutdownError("close "+s.config.ServerName+" connection", normalizeConnCloseError(s.conn.Close())),
		)
	}

	s.resetLocked()

	return closeErr
}

// resetLocked forgets process-bound resources so the same session wrapper can be started again.
func (s *session) resetLocked() {
	s.cmd = nil
	s.conn = nil
	s.fileWatcher = nil
	s.info = nil
	s.done = nil
	s.waitErr = nil
}
