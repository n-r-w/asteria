// Package consumer exercises cross-package reference shapes for gopls integration tests.
package consumer

import fixturelib "example.com/asteria-gopls-fixture/crosspkg/shared"

// PromotedBucket gives the fixture one embedded field that promotes a method from another package.
type PromotedBucket struct {
	fixturelib.EmbeddedBase
}

// UseAliasImport gives the fixture one alias-import selector reference to the shared factory.
func UseAliasImport(label string) string {
	bucket := fixturelib.MakeImportedBucket(label)

	return bucket.Describe()
}

// UsePromotedMethod gives the fixture one promoted method call back to the embedded declaration.
func UsePromotedMethod() string {
	bucket := PromotedBucket{}

	return bucket.DescribeEmbedded()
}

// UseInterfaceDispatch gives the fixture one interface-dispatch call to the shared contract method.
func UseInterfaceDispatch(label string) string {
	var contract fixturelib.Contract = fixturelib.MakeImportedBucket(label)

	return contract.Describe()
}
