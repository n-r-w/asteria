package appinit

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDIContainerCloseCallsRegisteredFuncs proves that application shutdown fans out to every registered
// adapter cleanup callback instead of leaving child LSP processes running after server exit.
func TestDIContainerCloseCallsRegisteredFuncs(t *testing.T) {
	t.Parallel()

	called := make([]string, 0, 2)
	//nolint:exhaustruct // This unit test exercises only CloseFuncs-driven shutdown behavior.
	di := &DIContainer{
		CloseFuncs: []CloseFunc{
			func(_ context.Context) error {
				called = append(called, "first")
				return nil
			},
			func(_ context.Context) error {
				called = append(called, "second")
				return nil
			},
		},
	}

	require.NoError(t, di.Close(t.Context()))
	assert.Equal(t, []string{"first", "second"}, called)
}

// TestDIContainerCloseJoinsErrors proves that shutdown keeps attempting every registered cleanup callback and
// returns the full error set back to the caller.
func TestDIContainerCloseJoinsErrors(t *testing.T) {
	t.Parallel()

	firstErr := errors.New("first close failed")
	secondErr := errors.New("second close failed")
	//nolint:exhaustruct // This unit test exercises only CloseFuncs-driven shutdown behavior.
	di := &DIContainer{
		CloseFuncs: []CloseFunc{
			nil,
			func(_ context.Context) error { return firstErr },
			func(_ context.Context) error { return secondErr },
		},
	}

	closeErr := di.Close(t.Context())
	require.ErrorIs(t, closeErr, firstErr)
	require.ErrorIs(t, closeErr, secondErr)
}
