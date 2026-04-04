// Package basic provides stable symbols for gopls integration tests.
package basic

// FixtureStamp gives the fixture one constant symbol.
const FixtureStamp = "v1"

// FixtureCounter gives the fixture one variable symbol.
//
//nolint:gochecknoglobals // the integration fixture needs a real package-level variable to exercise Variable kind.
var FixtureCounter = 1

// FixtureContract gives the fixture one interface symbol with method members.
type FixtureContract interface {
	MeasureDepth(depth int) int
	Describe() string
}

// FixtureBucket gives the integration tests a stable generic struct symbol with field members.
type FixtureBucket[T any] struct {
	Label string
	Value T
}

// MeasureDepth gives the fixture one pointer-receiver method symbol.
func (*FixtureBucket[T]) MeasureDepth(depth int) int {
	return depth + 1
}

// Describe gives the fixture one value-receiver method symbol.
func (b FixtureBucket[T]) Describe() string {
	return b.Label
}

// MakeBucket gives the fixture one generic function symbol.
func MakeBucket[T any](value T) FixtureBucket[T] {
	return FixtureBucket[T]{
		Label: FixtureStamp,
		Value: value,
	}
}

// makeBucketPrivate gives integration tests one lowercase function symbol for package-qualified lookup.
func makeBucketPrivate[T any](value T) FixtureBucket[T] {
	return FixtureBucket[T]{
		Label: FixtureStamp,
		Value: value,
	}
}
