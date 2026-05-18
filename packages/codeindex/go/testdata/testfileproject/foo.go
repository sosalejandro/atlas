// Package foo is a fixture for the include-test-files behaviour of the
// Go scanner. It is paired with foo_test.go so the scanner-test can
// assert what's indexed in the default (include-tests) vs.
// SkipTests=true modes.
package foo

// Foo is a placeholder production function. It must be indexed in both
// scan modes — its presence is the production-symbol smoke test.
func Foo() string {
	return "foo"
}

// FooReceiver is a placeholder receiver method so the test fixture also
// exercises method discovery, not only plain funcs.
type Receiver struct{}

func (r *Receiver) FooReceiver() string {
	return "foo-receiver"
}
