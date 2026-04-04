//go:build featurex

package buildtags

// UseVariantLabel gives the fixture one reference symbol for the featurex build variant.
func UseVariantLabel() string {
	return VariantLabel() + "-featurex"
}
