//go:build featurex

package buildtags

// UseTaggedOnlyTwice gives the fixture one symbol with two references to TaggedOnly.
func UseTaggedOnlyTwice(value string) string {
	first := TaggedOnly(value)

	return TaggedOnly(first)
}
