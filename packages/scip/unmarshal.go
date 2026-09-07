package scip

import (
	"fmt"

	upstream "github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

// unmarshal is a seam: the index is protobuf, and keeping the dependency in
// one function makes it obvious where a format change would land.
func unmarshal(data []byte, idx *upstream.Index) error {
	if err := proto.Unmarshal(data, idx); err != nil {
		// Wrapped rather than returned bare: protobuf's message says
		// "cannot parse invalid wire-format data", which does not tell a
		// user that the file they named is not a SCIP index at all -- the
		// overwhelmingly likely cause.
		return fmt.Errorf("not a SCIP index (protobuf): %w", err)
	}
	return nil
}
