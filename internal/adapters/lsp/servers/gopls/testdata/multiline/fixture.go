// Package multiline provides multiline reference layouts for gopls integration tests.
package multiline

// FixtureStamp gives the fixture one stable label value.
const FixtureStamp = "v1"

// FixtureBucket gives integration tests a stable generic type with one method reference target.
type FixtureBucket[T any] struct {
	Label string
	Value T
}

// Describe keeps one method reference target that can be called through a multiline chain.
func (b FixtureBucket[T]) Describe() string {
	return b.Label
}

// MakeBucket keeps one helper function that returns the fixture bucket for formatted call sites.
func MakeBucket[T any](value T) FixtureBucket[T] {
	return FixtureBucket[T]{
		Label: FixtureStamp,
		Value: value,
	}
}
