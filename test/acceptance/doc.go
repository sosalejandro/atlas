// Package acceptance is atlas's end-to-end layer: the whole pipeline over a
// real repository, asserting the numbers a user would read.
//
// # What separates this from the unit and property layers
//
// A unit test asks whether one function does what its name says. A property
// asks whether an invariant holds for any input. Neither can answer the
// question a buyer actually asks, which is whether the LAYERS COMPOSE — scan
// finds the symbols, ingest keeps their spans, the coverage ingest charges
// statements to them, the audit scores features from those charges — and
// whether the number that falls out the end is the same number an independent
// tool computes about the same run.
//
// So nothing here is mocked. The fixture under testdata/shopfixture is a real
// Go module with real tests; cover.coverprofile is the untouched output of
// running them; and the per-symbol fractions atlas produces are compared
// against `go tool cover -func` invoked live on that same profile. When atlas
// and the Go toolchain disagree about a fixture this small, atlas is wrong.
//
// # Layout
//
//	pipeline_test.go   the library pipeline, pinned to go tool cover
//	cli_test.go        the same repo through the real binary and --json
//	dogfood_test.go    atlas run against atlas (build tag: dogfood)
//	run.sh             the runner CI calls for the dogfood layer
//
// # Cost
//
// pipeline_test.go and cli_test.go run on every `go test ./...`; the binary
// build in cli_test.go is the expensive part and is done once for the
// package. dogfood_test.go is behind a build tag because it needs a
// coverprofile for the whole atlas repo, and producing one means running the
// suite that would be running it — see docs/testing/strategy.md.
package acceptance
