package contract

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sosalejandro/atlas/packages/codeindex"
	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

func openContractStore(t *testing.T) *store.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "atlas-state.db")
	s, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestPersist_RoundTripsAnnotatedContracts(t *testing.T) {
	t.Parallel()
	s := openContractStore(t)
	ctx := context.Background()

	idx := indexTestProject(t, "testdata/huma")
	// Ingest symbols + edges first so feature_symbols links can resolve.
	if _, err := s.Ingest(ctx, idx); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	e := NewExtractor(Options{ProjectRoot: "testdata/huma", SkipGraphQL: true, SkipTS: true})
	res, err := e.Extract(ctx, idx)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	stats, err := Persist(ctx, s, res.Defs, PersistOptions{AutoGenerateIDs: true})
	if err != nil {
		t.Fatalf("Persist: %v", err)
	}
	if stats.FeaturesUpserted == 0 {
		t.Errorf("expected at least one feature upserted, got 0")
	}

	// One annotated contract: platform.auth.login.
	feats, err := s.Features().List(ctx, store.FeatureFilter{
		Kind: ptrFeatureKind(store.FeatureKindContract),
	})
	if err != nil {
		t.Fatalf("Features.List: %v", err)
	}
	if len(feats) < 2 {
		t.Errorf("expected >= 2 contracts, got %d: %+v", len(feats), feats)
	}

	// Idempotency: re-running Persist must not duplicate.
	prevCount := len(feats)
	stats2, err := Persist(ctx, s, res.Defs, PersistOptions{AutoGenerateIDs: true})
	if err != nil {
		t.Fatalf("re-Persist: %v", err)
	}
	feats2, _ := s.Features().List(ctx, store.FeatureFilter{
		Kind: ptrFeatureKind(store.FeatureKindContract),
	})
	if len(feats2) != prevCount {
		t.Errorf("re-Persist duplicated rows: before=%d after=%d", prevCount, len(feats2))
	}
	_ = stats2
}

func TestPersist_SkipsWhenNoFeatureIDAndAutogenDisabled(t *testing.T) {
	t.Parallel()
	s := openContractStore(t)
	ctx := context.Background()

	defs := []ContractDef{
		{Kind: KindFunc, Name: "Whatever", Signature: "func Whatever()"},
	}
	stats, err := Persist(ctx, s, defs, PersistOptions{AutoGenerateIDs: false})
	if err != nil {
		t.Fatalf("Persist: %v", err)
	}
	if stats.FeaturesUpserted != 0 {
		t.Errorf("expected 0 features upserted when annotation absent + autogen off, got %d", stats.FeaturesUpserted)
	}
	if stats.Skipped != 1 {
		t.Errorf("expected 1 skipped, got %d", stats.Skipped)
	}
}

func TestPersist_SynthFeatureIDShape(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		def  ContractDef
		want shared.FeatureID
	}{
		{
			name: "huma-op",
			def: ContractDef{
				Kind: KindHumaOp,
				Operation: OperationDetail{
					OperationID: "createPlatformSubscription",
				},
			},
			want: "contract.huma.createplatformsubscription",
		},
		{
			name: "route",
			def: ContractDef{
				Kind: KindRoute,
				Operation: OperationDetail{
					Method: "POST",
					Path:   "/api/v1/auth/login",
				},
			},
			want: "contract.route.post.api.v1.auth.login",
		},
		{
			name: "graphql",
			def: ContractDef{
				Kind: KindGraphQL,
				Name: "Mutation.loginUser",
			},
			want: "contract.graphql.mutation.loginuser",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := synthFeatureID(tc.def, "contract")
			if got != tc.want {
				t.Errorf("synthFeatureID = %q, want %q", got, tc.want)
			}
		})
	}
}

func ptrFeatureKind(k store.FeatureKind) *store.FeatureKind { return &k }

// pull in codeindex anchor so the import survives goimports.
var _ = codeindex.IndexProject
