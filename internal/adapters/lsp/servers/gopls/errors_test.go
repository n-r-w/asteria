package lspgopls

import (
	"errors"
	"testing"

	"github.com/n-r-w/asteria/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSanitizeGoplsPublicErrorReturnsBuildConfigMessage proves that tagged Go files outside the active build
// graph surface one safe public error instead of leaking raw gopls internals.
func TestSanitizeGoplsPublicErrorReturnsBuildConfigMessage(t *testing.T) {
	t.Parallel()

	rootCause := errors.New(
		`request references for "tagged_only_featurex.go": no package metadata for file file:///tmp/tagged_only_featurex.go`,
	)

	err := sanitizeGoplsPublicError("file_path", "tagged_only_featurex.go", rootCause)
	require.Error(t, err)
	require.EqualError(
		t,
		err,
		`file_path "tagged_only_featurex.go" is excluded from the active Go build configuration; include its build tags in the active build flags`,
	)
	require.ErrorIs(t, err, rootCause)

	safeErr, ok := errors.AsType[*domain.SafeError](err)
	require.True(t, ok)
	assert.Same(t, rootCause, safeErr.Cause())
}

// TestSanitizeGoplsPublicErrorKeepsExplicitSafeMessages proves that adapter-local classification does not rewrite
// safe public errors that shared layers already prepared.
func TestSanitizeGoplsPublicErrorKeepsExplicitSafeMessages(t *testing.T) {
	t.Parallel()

	safeErr := domain.NewSafeError("no symbol matches", errors.New("internal detail"))

	assert.Same(t, safeErr, sanitizeGoplsPublicError("file_path", "fixture.go", safeErr))
}
