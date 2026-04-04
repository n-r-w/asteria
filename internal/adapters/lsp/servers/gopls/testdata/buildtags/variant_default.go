//go:build !featurex

package buildtags

// VariantLabel gives the fixture one symbol for the default build variant.
func VariantLabel() string {
	return "default"
}
