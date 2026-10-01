package runtimelsp

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type idleSuite struct {
	suite.Suite
}

func TestIdleSuite(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(idleSuite))
}

// TestActiveRequestsAndExpiry verifies that parallel workflows retain a session and reset
// its idle deadline only after the last workflow finishes. It uses no live LSP server.
func (s *idleSuite) TestActiveRequestsAndExpiry() {
	synctest.Test(s.T(), func(t *testing.T) {
		runtime := newTestRuntime(t, nil)
		runtime.config.SessionIdleTimeout = time.Second
		root := t.TempDir()
		firstSession, err := runtime.getOrCreateSession(root)
		require.NoError(t, err)
		firstRelease, err := runtime.AcquireSession(t.Context(), root)
		require.NoError(t, err)
		secondRelease, err := runtime.AcquireSession(t.Context(), root)
		require.NoError(t, err)

		time.Sleep(2 * time.Second)
		firstRelease()
		time.Sleep(2 * time.Second)
		require.Equal(t, 1, runtime.sessionCount())
		secondRelease()
		time.Sleep(900 * time.Millisecond)
		require.Equal(t, 1, runtime.sessionCount())
		time.Sleep(100 * time.Millisecond)
		synctest.Wait()
		require.Zero(t, runtime.sessionCount())

		restartedSession, err := runtime.getOrCreateSession(root)
		require.NoError(t, err)
		assert.NotSame(t, firstSession, restartedSession)
		require.NoError(t, runtime.Close(t.Context()))
	})
}

// TestReuseResetsIdleDeadline verifies reuse shortly before expiry. The old deadline
// must not retire a session while the next workflow holds it.
func (s *idleSuite) TestReuseResetsIdleDeadline() {
	synctest.Test(s.T(), func(t *testing.T) {
		runtime := newTestRuntime(t, nil)
		runtime.config.SessionIdleTimeout = time.Second
		root := t.TempDir()
		_, err := runtime.getOrCreateSession(root)
		require.NoError(t, err)
		release, err := runtime.AcquireSession(t.Context(), root)
		require.NoError(t, err)
		release()
		time.Sleep(900 * time.Millisecond)
		release, err = runtime.AcquireSession(t.Context(), root)
		require.NoError(t, err)
		time.Sleep(time.Second)
		require.Equal(t, 1, runtime.sessionCount())
		release()
		time.Sleep(time.Second)
		synctest.Wait()
		require.Zero(t, runtime.sessionCount())
		require.NoError(t, runtime.Close(t.Context()))
	})
}

func (r *Runtime) sessionCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sessions)
}

// TestRequestDuringRetirement verifies that a request waits for the old session's
// bounded shutdown, while cancellation interrupts that wait. No live LSP is needed.
func (s *idleSuite) TestRequestDuringRetirement() {
	synctest.Test(s.T(), func(t *testing.T) {
		runtime := newTestRuntime(t, nil)
		runtime.config.SessionIdleTimeout = time.Second
		root := t.TempDir()
		oldSession, err := runtime.getOrCreateSession(root)
		require.NoError(t, err)
		done := make(chan struct{})
		oldSession.done = done
		release, err := runtime.AcquireSession(t.Context(), root)
		require.NoError(t, err)
		release()
		time.Sleep(time.Second)
		synctest.Wait()

		requestCtx, cancel := context.WithCancel(t.Context())
		canceled := make(chan error, 1)
		go func() {
			_, acquireErr := runtime.AcquireSession(requestCtx, root)
			canceled <- acquireErr
		}()
		synctest.Wait()
		cancel()
		require.ErrorIs(t, <-canceled, context.Canceled)

		acquired := make(chan func(), 1)
		go func() {
			requestRelease, acquireErr := runtime.AcquireSession(t.Context(), root)
			assert.NoError(t, acquireErr)
			acquired <- requestRelease
		}()
		synctest.Wait()
		assert.Empty(t, acquired)
		close(done)
		release = <-acquired
		require.NotNil(t, release)
		newSession, err := runtime.getOrCreateSession(root)
		require.NoError(t, err)
		assert.NotSame(t, oldSession, newSession)
		release()
		require.NoError(t, runtime.Close(t.Context()))
	})
}
