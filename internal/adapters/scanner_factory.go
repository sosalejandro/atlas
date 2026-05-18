package adapters

import "github.com/sosalejandro/atlas/internal/ports"

// NewGraphBuilder selects the appropriate GraphBuilder based on config.
//
// When TypeChecking is enabled, it returns a TypedScanner that uses
// golang.org/x/tools/go/packages for full type resolution.
//
// EXPERIMENTAL: The TypedScanner does not yet integrate the route parser,
// Wire/Fx resolver, or SQLC mapper. It produces fewer nodes than GoASTScanner
// and uses significantly more memory (~4 GB for large workspaces). It is
// intended for struct field extraction in `testreg contract` only, not as
// a replacement for the default scanner. Use at your own risk.
//
// Default: GoASTScanner (fast, low memory, full feature parity).
func NewGraphBuilder(config ports.GraphConfig) ports.GraphBuilder {
	if config.TypeChecking {
		return NewTypedScanner()
	}
	return NewGoASTScanner()
}
