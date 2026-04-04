//go:build !featurex

package buildtags

// UseVariantLabel gives the fixture one reference symbol for the default build variant.
func UseVariantLabel() string {
	return VariantLabel() + "-default"
}
