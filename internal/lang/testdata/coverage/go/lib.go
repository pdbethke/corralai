package corralfixture

// Add is called by the fixture's test.
func Add(a, b int) int {
	return a + b
}

// Sign has a branch the test never takes.
func Sign(n int) int {
	if n < 0 {
		return -1
	}
	return 1
}

// Never is not called by any test.
func Never() int {
	x := 1
	y := 2
	return x + y
}

// Empty has no statements, so its block holds zero of them.
func Empty() {}
