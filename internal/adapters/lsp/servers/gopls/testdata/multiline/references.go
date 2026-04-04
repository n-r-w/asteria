package multiline

// UseDescribeAcrossLines keeps one multiline fluent call so integration tests can verify that
// one representative reference range stays single-line while the returned content spans context lines.
func UseDescribeAcrossLines(value string) string {
	return MakeBucket(
		value,
	).
		Describe()
}
