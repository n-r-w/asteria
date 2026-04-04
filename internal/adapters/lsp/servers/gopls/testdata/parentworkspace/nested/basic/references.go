package basic

// UseMakeBucketTwice gives integration tests one symbol with two references to MakeBucket.
func UseMakeBucketTwice(value string) string {
	left := MakeBucket(value)
	right := MakeBucket(left.Describe())

	return right.Describe()
}

// UseMakeBucketOnce gives integration tests one symbol with one reference to MakeBucket.
func UseMakeBucketOnce(value string) FixtureBucket[string] {
	return MakeBucket(value)
}
