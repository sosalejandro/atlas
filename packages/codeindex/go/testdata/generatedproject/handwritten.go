package app

// Handwritten is ordinary production code: no generated marker, no
// generated directory, no glob match. It must survive every exclusion
// rule the scanner applies.
func Handwritten() string {
	return "hand"
}
