// Package shared provides cross-package symbols for gopls integration tests.
package shared

// EmbeddedBase gives the fixture one embedded type whose method can be promoted across packages.
type EmbeddedBase struct{}

// DescribeEmbedded gives the fixture one promoted-method target for embedding assertions.
func (EmbeddedBase) DescribeEmbedded() string {
	return "embedded"
}

// ImportedBucket gives the fixture one imported struct symbol with one method.
type ImportedBucket struct {
	Label string
}

// Describe gives the fixture one cross-package method target on a non-embedded struct.
func (b ImportedBucket) Describe() string {
	return b.Label
}

// AliasBucket gives the fixture one type alias that resolves to the imported struct declaration.
type AliasBucket = ImportedBucket

// Contract gives the fixture one interface whose method can be referenced through interface dispatch.
type Contract interface {
	Describe() string
}

// MakeImportedBucket gives the fixture one cross-package factory used through an alias import.
func MakeImportedBucket(label string) ImportedBucket {
	return ImportedBucket{Label: label}
}
