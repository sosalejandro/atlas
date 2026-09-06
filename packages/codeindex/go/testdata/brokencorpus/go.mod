// brokencorpus is a module that does NOT compile, on purpose.
//
// Atlas runs mid-edit. The most valuable moment to ask "what does this
// change reach?" is the moment the tree is half-refactored and `go build`
// is red, so the typed resolver has to degrade rather than fail -- and it
// has to degrade PER PACKAGE, because a repo with one broken package
// still has two hundred good ones whose calls are exactly resolvable.
//
// The `broken` package here has a type error; `sound` does not, and does
// not import `broken`. A scan of this tree must type-check `sound`,
// report `broken` as degraded, and still return a graph.
module example.com/broken

go 1.25.0
