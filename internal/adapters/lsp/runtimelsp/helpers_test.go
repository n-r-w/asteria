package runtimelsp

import (
	"errors"
	"fmt"
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
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
