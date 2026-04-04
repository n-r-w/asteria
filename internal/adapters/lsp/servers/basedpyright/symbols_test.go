package lspbasedpyright

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestShouldIgnoreDir proves that Python-specific noise directories do not participate in workspace traversal.
func TestShouldIgnoreDir(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name         string
		relativePath string
		expected     bool
	}{
		{name: "hidden directory", relativePath: ".venv", expected: true},
		{name: "dotenv directory", relativePath: "pkg/.env", expected: true},
		{name: "pixi directory", relativePath: "pkg/.pixi", expected: true},
		{name: "pycache directory", relativePath: "pkg/__pycache__", expected: true},
		{name: "venv directory", relativePath: "pkg/venv", expected: true},
		{name: "build directory", relativePath: "pkg/build", expected: true},
		{name: "regular package", relativePath: "pkg/service", expected: false},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, testCase.expected, shouldIgnoreDir(testCase.relativePath))
		})
	}
}
