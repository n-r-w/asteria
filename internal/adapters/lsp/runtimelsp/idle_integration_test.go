//go:build integration_tests

package runtimelsp

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type idleIntegrationSuite struct {
	suite.Suite
}

func TestIntegrationIdleSuite(t *testing.T) {
	suite.Run(t, new(idleIntegrationSuite))
}

// TestProcessLifecycle verifies real gopls process retention, shutdown after the last
// request, and a fresh connection on reuse. It depends on gopls in PATH and Go fixtures.
func (s *idleIntegrationSuite) TestProcessLifecycle() {
	t := s.T()
	runtime := newIntegrationRuntime(t)
	runtime.config.SessionIdleTimeout = 100 * time.Millisecond
	root := runtimeFixtureRoot(t)
	ctx := t.Context()
	t.Cleanup(func() { require.NoError(t, runtime.Close(ctx)) })

	release, err := runtime.AcquireSession(ctx, root)
	require.NoError(t, err)
	firstConn, err := runtime.EnsureConn(ctx, root)
	require.NoError(t, err)
	firstSession, err := runtime.getOrCreateSession(root)
	require.NoError(t, err)
	time.Sleep(2 * runtime.config.SessionIdleTimeout)
	assert.True(t, sessionAlive(firstSession))
	release()
	require.Eventually(t, func() bool {
		return !sessionAlive(firstSession) && runtime.sessionCount() == 0
	}, 10*time.Second, 10*time.Millisecond)
	assert.Nil(t, firstSession.connection())

	release, err = runtime.AcquireSession(ctx, root)
	require.NoError(t, err)
	defer release()
	restartedConn, err := runtime.EnsureConn(ctx, root)
	require.NoError(t, err)
	assert.NotSame(t, firstConn, restartedConn)
	secondSession, err := runtime.getOrCreateSession(root)
	require.NoError(t, err)
	assert.True(t, sessionAlive(secondSession))
}
