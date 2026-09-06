package affected

import (
	"context"
	"fmt"

	"github.com/sosalejandro/atlas/packages/indexfresh"
	"github.com/sosalejandro/atlas/packages/store"
)

// FreshnessSource answers the question every line-number join in this package
// depends on: do the spans atlas stored for this file still describe the file
// git is diffing?
//
// It is a required input, not an optional one. The line numbers come from the
// working tree at HEAD; symbols.line/end_line come from whenever `atlas scan`
// last ran. If an earlier hunk shifted a file since that scan, a changed line
// resolves to whichever symbol USED to occupy those lines — so `affected`
// selects that symbol's tests and omits the tests of the symbol the diff
// actually edited. Test selection that silently drops the relevant test is
// the one failure this command must never have, and it is invisible without
// this check.
type FreshnessSource interface {
	// Classify returns a state per repo-relative path. Only
	// indexfresh.StateCurrent permits a span join.
	Classify(ctx context.Context, paths []string) (indexfresh.Report, error)
}

// NewFreshness adapts the store's file_hashes port to FreshnessSource, hashing
// files under root. This is what production wires in; tests supply their own.
func NewFreshness(hashes store.FileHashes, root string) FreshnessSource {
	return &storeFreshness{hashes: hashes, root: root}
}

type storeFreshness struct {
	hashes store.FileHashes
	root   string
}

func (f *storeFreshness) Classify(ctx context.Context, paths []string) (indexfresh.Report, error) {
	rep, err := indexfresh.Classify(ctx, f.hashes, f.root, paths)
	if err != nil {
		return indexfresh.Report{}, fmt.Errorf("affected: classify index freshness: %w", err)
	}
	return rep, nil
}
