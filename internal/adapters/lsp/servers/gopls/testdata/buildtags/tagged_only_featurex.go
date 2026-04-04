//go:build featurex

// Package buildtags provides build-tag-specific fixtures for gopls integration tests.
package buildtags

// TaggedOnly gives the fixture one symbol that exists only in the featurex build.
func TaggedOnly(value string) string {
	return value + "-featurex"
}
