// Test file paired with foo.go — load-bearing for the include-tests
// regression suite. The test-file scanner test expects:
//
//   - default scan: both TestFoo + TestFooFeature are present in the
//     graph, AND the @atlas:feature annotation on TestFooFeature is
//     attributed to it.
//   - SkipTests=true: neither TestFoo nor TestFooFeature appears.
package foo

import "testing"

// TestFoo is a vanilla top-level test function. Its only job is to be
// indexable by the Go AST scanner — proving that test funcs participate
// in the call graph by default.
func TestFoo(_ *testing.T) {
	_ = Foo()
}

// @atlas:feature foo.bar
//
// TestFooFeature carries the canonical annotation pattern Atlas is built
// around: the feature ID is attached to the TEST that proves the feature
// works, not to the production handler the test exercises. The scanner
// must surface the annotation AND point its Symbol.Position at this
// function's source location.
func TestFooFeature(_ *testing.T) {
	_ = Foo()
}
