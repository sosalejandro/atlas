// A miniature module used by the shim's acceptance test: it is the smallest
// thing that can prove `atlas cov shim init` writes a TestMain that compiles
// and that the resulting suite emits one counter snapshot per test.
//
// It lives under testdata so the go tool ignores it during ordinary builds,
// and it reaches the shim through a relative replace rather than a released
// version so the test exercises the working tree.
module example.com/covfixture

go 1.25.0

require github.com/sosalejandro/atlas v0.0.0

replace github.com/sosalejandro/atlas => ../../../../..
