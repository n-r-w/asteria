package lsprustanalyzer

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestRawRustQueryPath keeps canonical Rust member queries aligned with the raw impl-container shape that
// rust-analyzer exposes, including nested module paths.
func TestRawRustQueryPath(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name            string
		namePath        string
		expectedPath    string
		expectedChanged bool
	}{
		{
			name:            "top-level impl member",
			namePath:        "Bucket/describe",
			expectedPath:    "impl Bucket/describe",
			expectedChanged: true,
		},
		{
			name:            "nested impl member",
			namePath:        "module/Bucket/describe",
			expectedPath:    "module/impl Bucket/describe",
			expectedChanged: true,
		},
		{
			name:            "already raw impl member",
			namePath:        "module/impl Bucket/describe",
			expectedPath:    "module/impl Bucket/describe",
			expectedChanged: false,
		},
		{
			name:            "non-member path",
			namePath:        "make_bucket",
			expectedPath:    "make_bucket",
			expectedChanged: false,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			actualPath, actualChanged := rawRustQueryPath(testCase.namePath)

			assert.Equal(t, testCase.expectedPath, actualPath)
			assert.Equal(t, testCase.expectedChanged, actualChanged)
		})
	}
}

// TestShouldIgnoreDir keeps Rust workspace traversal away from hidden directories and Cargo build output
// while still allowing source trees to participate in symbolic search.
func TestShouldIgnoreDir(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name         string
		relativePath string
		expected     bool
	}{
		{name: "hidden directory", relativePath: ".cargo", expected: true},
		{name: "nested hidden directory", relativePath: "crate/.git", expected: true},
		{name: "target directory", relativePath: "crate/target", expected: true},
		{name: "source directory", relativePath: "src", expected: false},
		{name: "nested source directory", relativePath: "src/nested", expected: false},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, testCase.expected, shouldIgnoreDir(testCase.relativePath))
		})
	}
}
