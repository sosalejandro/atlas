// This module exists so the corpus can be TYPE-CHECKED, not so it can be
// run. Issue #87 moved Go call resolution onto go/packages, and go/packages
// needs a real module boundary and a build that succeeds -- so the fixture
// that pins the scanner's output has to be one.
//
// It is a separate module from atlas on purpose: it lives under testdata/,
// which the go tool skips, so `go build ./...` at the repo root never sees
// it and `go test ./packages/codeindex/go` loads it explicitly by path.
module example.com/orderd

go 1.25.0
