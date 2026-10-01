package runtimelsp

import (
	"context"
	"log/slog"
	"time"
)

// AcquireSession keeps a workspace session available for the complete caller workflow.
// The caller must release it exactly once, after all requests and document cleanup finish.
// Acquisition does not start the language server.
func (r *Runtime) AcquireSession(ctx context.Context, workspaceRoot string) (func(), error) {
	root, err := normalizeWorkspaceRoot(workspaceRoot)
	if err != nil {
		return nil, err
	}

	for {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		r.mu.Lock()
		if r.closing {
			r.mu.Unlock()
			return nil, errRuntimeClosing
		}
		_, err = r.getOrCreateSessionLocked(root)
		if err != nil {
			r.mu.Unlock()
			return nil, err
		}
		managed := r.sessions[root]
		if managed.closing != nil {
			closed := managed.closing
			r.mu.Unlock()
			select {
			case <-closed:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		managed.stopIdleTimer()
		managed.requests++
		r.active++
		r.mu.Unlock()

		return func() { r.releaseSession(root, managed) }, nil
	}
}

// releaseSession starts the idle deadline only after the last workflow finishes.
func (r *Runtime) releaseSession(root string, managed *managedSession) {
	r.mu.Lock()
	defer r.mu.Unlock()
	managed.requests--
	r.active--
	if r.sessions[root] == managed && managed.requests == 0 && managed.closing == nil {
		r.scheduleIdleSessionLocked(root, managed)
	}
}

// scheduleIdleSessionLocked requires r.mu and records the deadline for stale timer callbacks.
func (r *Runtime) scheduleIdleSessionLocked(root string, managed *managedSession) {
	managed.idleDeadline = time.Now().Add(r.config.SessionIdleTimeout)
	managed.idleTimer = time.AfterFunc(r.config.SessionIdleTimeout, func() {
		r.closeIdleSession(root, managed)
	})
}

// closeIdleSession keeps the retiring session registered until shutdown finishes,
// so a new request cannot start its replacement while the old process is stopping.
func (r *Runtime) closeIdleSession(root string, managed *managedSession) {
	r.mu.Lock()
	// Stop may leave a timer callback already waiting for this mutex. A newer
	// workflow's deadline must take precedence over that callback.
	if r.sessions[root] != managed || managed.requests != 0 ||
		managed.closing != nil || time.Now().Before(managed.idleDeadline) {
		r.mu.Unlock()
		return
	}
	if r.closing {
		r.scheduleIdleSessionLocked(root, managed)
		r.mu.Unlock()
		return
	}
	managed.closing = make(chan struct{})
	r.active++
	r.mu.Unlock()

	// Idle cleanup has no live request context. The session owns a bounded shutdown budget.
	ctx := context.Background()
	if err := managed.session.close(ctx); err != nil {
		slog.WarnContext(ctx, "close idle LSP session",
			"server_name", r.config.ServerName, "workspace_root", root, "err", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sessions[root] == managed {
		delete(r.sessions, root)
	}
	r.active--
	close(managed.closing)
}

// stopIdleTimer requires the owning runtime's mutex.
func (s *managedSession) stopIdleTimer() {
	if s.idleTimer != nil {
		s.idleTimer.Stop()
		s.idleTimer = nil
	}
}
