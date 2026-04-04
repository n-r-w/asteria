//go:build integration_tests

package runtimelsp

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// runtimeFixtureRoot returns the absolute path to the stable Go module used by runtime integration tests.
func runtimeFixtureRoot(t *testing.T) string {
	t.Helper()

	fixtureRoot, err := filepath.Abs(filepath.Join("..", "servers", "gopls", "testdata", "basic"))
	require.NoError(t, err)

	return fixtureRoot
}

// runtimeMultilineFixtureRoot returns the absolute path to the multiline Go fixture module.
func runtimeMultilineFixtureRoot(t *testing.T) string {
	t.Helper()

	fixtureRoot, err := filepath.Abs(filepath.Join("..", "servers", "gopls", "testdata", "multiline"))
	require.NoError(t, err)

	return fixtureRoot
}

// newIntegrationRuntime creates a runtime configured against gopls so lifecycle tests can stay package-local.
func newIntegrationRuntime(t *testing.T) *Runtime {
	t.Helper()

	runtime, err := New(&RuntimeConfig{
		LSPConfig: LSPConfig{
			Command:                 "gopls",
			Args:                    nil,
			ServerName:              "gopls",
			ShutdownTimeout:         0,
			ReplyConfiguration:      nil,
			BuildClientCapabilities: nil,
		},
		BuildWorkspaceFolders: nil,
	})
	require.NoError(t, err)

	return runtime
}

// sessionAlive keeps runtime integration assertions out of production code while still checking live session state.
func sessionAlive(s *session) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.isAliveLocked()
}
