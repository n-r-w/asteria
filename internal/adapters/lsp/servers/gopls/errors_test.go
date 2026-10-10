package lspgopls

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/n-r-w/asteria/internal/domain"
)

// TestExplainGoplsBuildTagErrorReturnsBuildConfigMessage proves that tagged Go files outside the active build
// graph explain how to fix the build flags.
func TestExplainGoplsBuildTagErrorReturnsBuildConfigMessage(t *testing.T) {
	t.Parallel()

	rootCause := errors.New(
		`request references for "tagged_only_featurex.go": no package metadata for file file:///tmp/tagged_only_featurex.go`,
	)

	err := explainGoplsBuildTagError("file_path", "tagged_only_featurex.go", rootCause)
	require.Error(t, err)
	require.EqualError(
		t,
		err,
		`file_path "tagged_only_featurex.go" is excluded from the active Go build configuration; `+
			`include its build tags in the active build flags: `+rootCause.Error(),
	)
	require.ErrorIs(t, err, rootCause)

	toolErr, ok := errors.AsType[*domain.ToolError](err)
	require.True(t, ok)
	assert.Same(t, rootCause, toolErr.Cause())
}

// TestExplainGoplsBuildTagErrorKeepsExistingToolError proves that the adapter keeps errors that already have a message.
func TestExplainGoplsBuildTagErrorKeepsExistingToolError(t *testing.T) {
	t.Parallel()

	toolErr := domain.NewToolError("no symbol matches", errors.New("internal detail"))

	assert.Same(t, toolErr, explainGoplsBuildTagError("file_path", "fixture.go", toolErr))
}
