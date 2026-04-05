package runtimelsp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeConnCloseErrorIgnoresExpectedCloseNoise(t *testing.T) {
	t.Parallel()

	err := errors.Join(
		fmt.Errorf("close writer: %w", os.ErrClosed),
		errors.Join(os.ErrClosed, fmt.Errorf("close reader: %w", os.ErrClosed)),
	)

	assert.NoError(t, normalizeConnCloseError(err))
}

func TestNormalizeConnCloseErrorKeepsUnexpectedCloseFailure(t *testing.T) {
	t.Parallel()

	err := errors.Join(
		fmt.Errorf("close writer: %w", os.ErrClosed),
		io.EOF,
	)

	assert.ErrorIs(t, normalizeConnCloseError(err), io.EOF)
}

// TestNormalizeShutdownRPCErrorIgnoresExpectedCloseNoise proves that shutdown and exit requests
// stay quiet when the server has already closed the transport during teardown.
func TestNormalizeShutdownRPCErrorIgnoresExpectedCloseNoise(t *testing.T) {
	t.Parallel()

	err := errors.Join(
		fmt.Errorf("shutdown request: %w", os.ErrClosed),
		fmt.Errorf("exit request: %w", os.ErrClosed),
	)

	assert.NoError(t, normalizeShutdownRPCError(err))
}

// TestNormalizeGracefulShutdownErrorIgnoresTimeoutAfterForcedStop proves that an eventual forced stop
// is enough to make a slow graceful shutdown non-fatal during cleanup.
func TestNormalizeGracefulShutdownErrorIgnoresTimeoutAfterForcedStop(t *testing.T) {
	t.Parallel()

	require.NoError(t, normalizeGracefulShutdownError(context.DeadlineExceeded, true))
	assert.ErrorIs(t, normalizeGracefulShutdownError(context.DeadlineExceeded, false), context.DeadlineExceeded)
}
