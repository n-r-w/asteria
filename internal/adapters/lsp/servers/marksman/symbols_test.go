package lspmarksman

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestLanguageIDForExtension keeps the didOpen language identifier stable for both Markdown extensions.
func TestLanguageIDForExtension(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "md extension", input: ".md", expected: markdownLanguageID},
		{name: "markdown extension", input: ".markdown", expected: markdownLanguageID},
		{name: "unknown extension defaults to markdown", input: ".txt", expected: markdownLanguageID},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, testCase.expected, languageIDForExtension(testCase.input))
		})
	}
}
