package main

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunServerLifecycleClosesWithUncancelledContext proves that the main shutdown path strips signal
// cancellation before calling adapter cleanup, so LSP processes still get a chance to exit cleanly.
func TestRunServerLifecycleClosesWithUncancelledContext(t *testing.T) {
	t.Parallel()

	closeCalled := false
	closeContextCancelled := false

	err := runServerLifecycle(
		t.Context(),
		func(context.Context) error { return nil },
		func(ctx context.Context) error {
			closeCalled = true
			closeContextCancelled = ctx.Err() != nil
			return nil
		},
	)
	require.NoError(t, err)
	assert.True(t, closeCalled)
	assert.False(t, closeContextCancelled)
}

// TestRunServerLifecycleJoinsRunAndCloseErrors proves that shutdown cleanup errors are not lost when the server run
// path itself also fails.
func TestRunServerLifecycleJoinsRunAndCloseErrors(t *testing.T) {
	t.Parallel()

	runErr := errors.New("run failed")
	closeErr := errors.New("close failed")

	err := runServerLifecycle(
		t.Context(),
		func(context.Context) error { return runErr },
		func(context.Context) error { return closeErr },
	)
	require.ErrorIs(t, err, runErr)
	require.ErrorIs(t, err, closeErr)
}
