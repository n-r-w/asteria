package lspgopls

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestBuildNamePathNormalizesGoMethodReceivers proves that Go method symbols stay addressable
// with canonical name paths instead of raw receiver prefixes from gopls.
func TestBuildNamePathNormalizesGoMethodReceivers(t *testing.T) {
	t.Parallel()

	require.Equal(t, "FixtureBucket/Describe", buildNamePath("", "FixtureBucket[T].Describe"))
	require.Equal(t, "FixtureBucket/MeasureDepth", buildNamePath("", "(*FixtureBucket[T]).MeasureDepth"))
	require.Equal(t, "FixtureBucket/describe", buildNamePath("", "(*FixtureBucket[T]).describe"))
	require.Equal(t, "MakeBucket", buildNamePath("", "MakeBucket"))
}

// TestNormalizeGoQueryPath tolerates package-qualified Go queries so callers do not need prompt-level exceptions.
func TestNormalizeGoQueryPath(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "plain symbol", input: "MakeBucket", expected: "MakeBucket"},
		{name: "absolute canonical path", input: "/FixtureBucket/Describe", expected: "/FixtureBucket/Describe"},
		{name: "slash package qualifier", input: "basic/MakeBucket", expected: "MakeBucket"},
		{name: "dot package qualifier", input: "basic.MakeBucket", expected: "MakeBucket"},
		{name: "lowercase slash package qualifier", input: "basic/makeBucketPrivate", expected: "makeBucketPrivate"},
		{name: "lowercase dot package qualifier", input: "basic.makeBucketPrivate", expected: "makeBucketPrivate"},
		{name: "slash package type method", input: "basic/FixtureBucket/Describe", expected: "FixtureBucket/Describe"},
		{name: "dot package type method", input: "basic.FixtureBucket/Describe", expected: "FixtureBucket/Describe"},
		{name: "lowercase slash package type method", input: "basic/fixtureBucket/describe", expected: "fixtureBucket/describe"},
		{name: "lowercase dot package type method", input: "basic.fixtureBucket/describe", expected: "fixtureBucket/describe"},
		{name: "absolute slash package qualifier stays unsupported", input: "/basic/MakeBucket", expected: "/basic/MakeBucket"},
		{name: "absolute dot package type method stays unsupported", input: "/basic.FixtureBucket/Describe", expected: "/basic.FixtureBucket/Describe"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, testCase.expected, normalizeGoQueryPath(testCase.input))
		})
	}
}

// TestShouldWatchGoFile keeps the gopls file-watch contract explicit for source and workspace-definition files.
func TestShouldWatchGoFile(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		input    string
		expected bool
	}{
		{name: "go source file", input: "fixture.go", expected: true},
		{name: "nested go source file", input: "pkg/nested/fixture.go", expected: true},
		{name: "go mod", input: "go.mod", expected: true},
		{name: "go sum", input: "go.sum", expected: true},
		{name: "go work", input: "go.work", expected: true},
		{name: "go work sum", input: "go.work.sum", expected: true},
		{name: "non go file", input: "fixture.txt", expected: false},
		{name: "hidden file with no go meaning", input: ".env", expected: false},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, testCase.expected, shouldWatchGoFile(testCase.input))
		})
	}
}
