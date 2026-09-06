// authoritycorpus is the fixture for ONE rule: on a file the type checker
// handled, the name ladder does not get a second opinion.
//
// Everything else in #87 is about what the typed resolver can answer. This
// module is about what it DECLINES to answer — calls into the standard
// library, which atlas does not index — and about what the name ladder
// would have invented in its place. The two shapes here are the two ways
// that invention shows up in a graph: a wrong edge into an unrelated
// indexed declaration, and a stub node for a symbol that was never
// scanned.
//
// It compiles. A fixture that stopped type-checking would make every
// assertion about the typed path pass vacuously, so the tests here also
// assert a positive control from the same file.
module example.com/authority

go 1.25.0
