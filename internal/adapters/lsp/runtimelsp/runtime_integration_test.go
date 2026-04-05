//go:build integration_tests

package runtimelsp

import (
	"os/exec"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.lsp.dev/jsonrpc2"
)

// TestIntegrationRuntimeEnsureConnIsLazyAndReused proves that the first live request starts a process
// and later requests reuse the same runtime session for the fixture workspace.
func TestIntegrationRuntimeEnsureConnIsLazyAndReused(t *testing.T) {
	runtime := newIntegrationRuntime(t)
	workspaceRoot := runtimeFixtureRoot(t)

	ctx := t.Context()
	t.Cleanup(func() {
		require.NoError(t, runtime.Close(ctx))
	})

	firstConn, err := runtime.EnsureConn(ctx, workspaceRoot)
	require.NoError(t, err)
	require.NotNil(t, firstConn)
	session, sessionErr := runtime.getOrCreateSession(workspaceRoot)
	require.NoError(t, sessionErr)
	assert.Equal(t, workspaceRoot, session.config.WorkspaceRoot)

	secondConn, err := runtime.EnsureConn(ctx, workspaceRoot)
	require.NoError(t, err)
	assert.Same(t, firstConn, secondConn)
	assert.Same(t, session.connection(), secondConn)
	require.Len(t, runtime.sessions, 1)
}

// TestIntegrationRuntimeCloseClosesActiveSession proves that the runtime exposes graceful shutdown for a real process.
func TestIntegrationRuntimeCloseClosesActiveSession(t *testing.T) {
	runtime := newIntegrationRuntime(t)
	workspaceRoot := runtimeFixtureRoot(t)

	ctx := t.Context()
	t.Cleanup(func() {
		require.NoError(t, runtime.Close(ctx))
	})

	liveConn, err := runtime.EnsureConn(ctx, workspaceRoot)
	require.NoError(t, err)
	require.NotNil(t, liveConn)
	session, sessionErr := runtime.getOrCreateSession(workspaceRoot)
	require.NoError(t, sessionErr)

	require.NoError(t, runtime.Close(ctx))
	assert.False(t, sessionAlive(session))
	assert.Nil(t, session.connection())
	require.Empty(t, runtime.sessions)
}

// TestIntegrationRuntimeEnsureConnRestartsAfterClose proves that a closed live session can start a new process on the next access.
func TestIntegrationRuntimeEnsureConnRestartsAfterClose(t *testing.T) {
	runtime := newIntegrationRuntime(t)
	workspaceRoot := runtimeFixtureRoot(t)

	ctx := t.Context()
	t.Cleanup(func() {
		require.NoError(t, runtime.Close(ctx))
	})

	_, err := runtime.EnsureConn(ctx, workspaceRoot)
	require.NoError(t, err)
	firstSession, firstErr := runtime.getOrCreateSession(workspaceRoot)
	require.NoError(t, firstErr)
	firstCmd := firstSession.cmd
	require.NotNil(t, firstCmd)
	require.NoError(t, runtime.Close(ctx))
	assert.False(t, sessionAlive(firstSession))

	secondConn, err := runtime.EnsureConn(ctx, workspaceRoot)
	require.NoError(t, err)
	assert.NotNil(t, secondConn)
	secondSession, secondErr := runtime.getOrCreateSession(workspaceRoot)
	require.NoError(t, secondErr)
	assert.NotSame(t, firstSession, secondSession)
	assert.NotNil(t, secondSession.cmd)
	assert.NotSame(t, firstCmd, secondSession.cmd)
	assert.True(t, sessionAlive(secondSession))
}

// TestIntegrationRuntimeEnsureConnRecoversFromStaleState proves that leftover process state does not block the next live startup.
func TestIntegrationRuntimeEnsureConnRecoversFromStaleState(t *testing.T) {
	runtime := newIntegrationRuntime(t)
	workspaceRoot := runtimeFixtureRoot(t)

	ctx := t.Context()
	staleConfig, configErr := runtime.newSessionConfig(workspaceRoot)
	require.NoError(t, configErr)
	normalizedWorkspaceRoot, rootErr := normalizeWorkspaceRoot(workspaceRoot)
	require.NoError(t, rootErr)
	runtime.sessions[normalizedWorkspaceRoot] = &session{
		config:     staleConfig,
		cmd:        &exec.Cmd{},
		conn:       nil,
		done:       nil,
		waitResult: nil,
	}
	t.Cleanup(func() {
		require.NoError(t, runtime.Close(ctx))
	})

	recoveredConn, err := runtime.EnsureConn(ctx, workspaceRoot)
	require.NoError(t, err)
	assert.NotNil(t, recoveredConn)
	recoveredSession, sessionErr := runtime.getOrCreateSession(workspaceRoot)
	require.NoError(t, sessionErr)
	assert.NotNil(t, recoveredSession.cmd)
	assert.True(t, sessionAlive(recoveredSession))
}

// TestIntegrationRuntimeEnsureConnIsolatedByRoot proves that one runtime keeps separate live sessions per root.
func TestIntegrationRuntimeEnsureConnIsolatedByRoot(t *testing.T) {
	runtime := newIntegrationRuntime(t)
	firstRoot := runtimeFixtureRoot(t)
	secondRoot := runtimeMultilineFixtureRoot(t)

	ctx := t.Context()
	t.Cleanup(func() {
		require.NoError(t, runtime.Close(ctx))
	})

	firstConn, firstErr := runtime.EnsureConn(ctx, firstRoot)
	require.NoError(t, firstErr)
	secondConn, secondErr := runtime.EnsureConn(ctx, secondRoot)
	require.NoError(t, secondErr)

	assert.NotSame(t, firstConn, secondConn)
	require.Len(t, runtime.sessions, 2)

	firstSession, firstSessionErr := runtime.getOrCreateSession(firstRoot)
	require.NoError(t, firstSessionErr)
	secondSession, secondSessionErr := runtime.getOrCreateSession(secondRoot)
	require.NoError(t, secondSessionErr)
	assert.NotSame(t, firstSession, secondSession)

	require.NoError(t, runtime.Close(ctx))
	assert.False(t, sessionAlive(firstSession))
	assert.False(t, sessionAlive(secondSession))
	require.Empty(t, runtime.sessions)
}

// TestIntegrationRuntimeEnsureConnConcurrentSameRootReusesSingleSession proves that concurrent callers share one session for one root.
func TestIntegrationRuntimeEnsureConnConcurrentSameRootReusesSingleSession(t *testing.T) {
	runtime := newIntegrationRuntime(t)
	workspaceRoot := runtimeFixtureRoot(t)

	ctx := t.Context()
	t.Cleanup(func() {
		require.NoError(t, runtime.Close(ctx))
	})

	type ensureConnResult struct {
		conn jsonrpc2.Conn
		err  error
	}

	start := make(chan struct{})
	results := make(chan ensureConnResult, 2)
	var waitGroup sync.WaitGroup
	for range 2 {
		waitGroup.Go(func() {
			<-start
			conn, err := runtime.EnsureConn(ctx, workspaceRoot)
			results <- ensureConnResult{conn: conn, err: err}
		})
	}

	close(start)
	waitGroup.Wait()
	close(results)

	collectedResults := make([]ensureConnResult, 0, 2)
	for result := range results {
		collectedResults = append(collectedResults, result)
	}
	require.Len(t, collectedResults, 2)
	require.NoError(t, collectedResults[0].err)
	require.NoError(t, collectedResults[1].err)
	assert.Same(t, collectedResults[0].conn, collectedResults[1].conn)
	require.Len(t, runtime.sessions, 1)
}

// TestIntegrationRuntimeEnsureConnConcurrentDifferentRootsKeepsSeparateSessions proves that different roots can start concurrently without sharing a session.
func TestIntegrationRuntimeEnsureConnConcurrentDifferentRootsKeepsSeparateSessions(t *testing.T) {
	runtime := newIntegrationRuntime(t)
	firstRoot := runtimeFixtureRoot(t)
	secondRoot := runtimeMultilineFixtureRoot(t)

	ctx := t.Context()
	t.Cleanup(func() {
		require.NoError(t, runtime.Close(ctx))
	})

	type ensureConnResult struct {
		conn jsonrpc2.Conn
		err  error
	}

	start := make(chan struct{})
	results := make(chan ensureConnResult, 2)
	var waitGroup sync.WaitGroup
	for _, workspaceRoot := range []string{firstRoot, secondRoot} {
		workspaceRoot := workspaceRoot
		waitGroup.Go(func() {
			<-start
			conn, err := runtime.EnsureConn(ctx, workspaceRoot)
			results <- ensureConnResult{conn: conn, err: err}
		})
	}

	close(start)
	waitGroup.Wait()
	close(results)

	collectedResults := make([]ensureConnResult, 0, 2)
	for result := range results {
		collectedResults = append(collectedResults, result)
	}
	require.Len(t, collectedResults, 2)
	require.NoError(t, collectedResults[0].err)
	require.NoError(t, collectedResults[1].err)
	assert.NotSame(t, collectedResults[0].conn, collectedResults[1].conn)
	require.Len(t, runtime.sessions, 2)
}
