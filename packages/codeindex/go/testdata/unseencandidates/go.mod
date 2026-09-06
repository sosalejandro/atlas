// unseencandidates is an interface whose implementations atlas does not
// all index.
//
// The type checker sees both implementations of Writer; the scan indexes
// only one, because the other lives in a file the generated-code ledger
// excludes. That gap is the normal case in a real repository -- sqlc
// output, protobuf stubs, an implementation in a package that failed to
// type-check -- and it is exactly when an ambiguity flag matters most:
// the alternatives atlas cannot see are still alternatives.
module example.com/unseen

go 1.25.0
