package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"

	"github.com/danielgtaylor/huma/v2"
	"github.com/sosalejandro/atlas/packages/doctor"
	"github.com/sosalejandro/atlas/packages/envelope"
	"github.com/sosalejandro/atlas/packages/graph"
	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// The routes, and why they are declared through huma rather than registered
// on a mux by hand.
//
// A local API needs a boundary contract somebody can hold atlas to, and the
// contract has to be DERIVED rather than written beside the code, or it drifts
// exactly the way the testreg dashboard drifted from the CLI. huma emits
// OpenAPI 3.1 and JSON Schema from these Go types, so the description of the
// API is generated from the thing being described. That is the same principle
// as the rest of atlas: derived, not typed in.
//
// gRPC was considered and rejected for one reason specific to atlas. #101
// exists so the HTTP surface "returns the same envelope shape as --json so
// the two surfaces cannot drift"; a protobuf contract would give the API a
// second shape generated from a second source, which is that drift in a new
// hat. Every response below is an envelope.Envelope -- the same type
// internal/cli emits.
//
// A NOTE ON /api/health, because it deviates from #101 deliberately: that
// issue describes /api/health as "index trust: scan freshness, unindexed
// files, collisions", which is doctor's job. It was written before #112
// renamed `atlas audit` to `atlas health`, so the word now means feature
// health scores on the CLI. Two surfaces where `health` means different
// things is precisely the drift this package prevents, so the routes follow
// the CURRENT verbs: /api/doctor is index trust, matching `atlas doctor`.

// APITitle and APIVersion identify the generated OpenAPI document.
const (
	APITitle   = "Atlas local API"
	APIVersion = envelope.SchemaVersion
)

// body is the response wrapper huma renders. Every SUCCESSFUL operation
// returns the same envelope internal/cli emits, so the two surfaces answer
// in one shape.
//
// Errors are RFC 7807 problem+json rather than an envelope. That is huma's
// native error contract and it is described in the generated OpenAPI
// document, so a consumer still has one machine-readable shape to handle --
// it is simply a different one from the success case, and stated as such
// rather than left for somebody to discover.
type body struct {
	Body envelope.Envelope
}

// register wires every operation onto the huma API.
func (s *Server) register(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "get-meta",
		Method:      http.MethodGet,
		Path:        "/api/meta",
		Summary:     "Which index this is",
		Description: "The database path, the working tree it describes, and the " +
			"schema version, so a consumer holding two windows open can tell them apart.",
	}, s.opMeta)

	huma.Register(api, huma.Operation{
		OperationID: "get-doctor",
		Method:      http.MethodGet,
		Path:        "/api/doctor",
		Summary:     "Is this index trustworthy",
		Description: "Runs the same check set as `atlas doctor`, not a subset. A " +
			"consumer gating on this must reach the same verdict as one gating on " +
			"the CLI's exit code, or the two surfaces disagree about whether the " +
			"index can be trusted at all.",
	}, s.opDoctor)

	huma.Register(api, huma.Operation{
		OperationID: "list-features",
		Method:      http.MethodGet,
		Path:        "/api/features",
		Summary:     "Every declared feature",
	}, s.opFeatures)

	huma.Register(api, huma.Operation{
		OperationID: "get-feature",
		Method:      http.MethodGet,
		Path:        "/api/features/{id}",
		Summary:     "One feature",
	}, s.opFeature)

	huma.Register(api, huma.Operation{
		OperationID: "get-feature-graph",
		Method:      http.MethodGet,
		Path:        "/api/features/{id}/graph",
		Summary:     "The node/edge neighbourhood of one feature",
		Description: "Every edge carries the resolution tier that established it. A " +
			"renderer that draws all edges alike claims a uniform confidence the " +
			"index does not have, and the tier is the one thing atlas can put on a " +
			"diagram that a drawing tool cannot.",
	}, s.opGraph)
}

// wrap builds the response envelope, honouring Stable.
func (s *Server) wrap(command string, args, result any, warnings []string) (*body, error) {
	env := envelope.New(command, args, result, warnings,
		s.opts.Now().UTC().Format(timeLayout))
	if s.opts.Stable {
		stable, err := env.Stable()
		if err != nil {
			return nil, huma.Error500InternalServerError("stable form", err)
		}
		env = stable
	}
	return &body{Body: env}, nil
}

// ---------------------------------------------------------------------------
// meta

type metaInput struct{}

type metaResult struct {
	DBPath        string `json:"db_path" doc:"absolute path of the state database"`
	Root          string `json:"root" doc:"working tree the index describes"`
	SchemaVersion int    `json:"schema_version_db" doc:"applied migration version"`
}

func (s *Server) opMeta(ctx context.Context, _ *metaInput) (*body, error) {
	v, err := s.opts.Store.SchemaVersion(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("read schema version", err)
	}
	return s.wrap("api.meta", nil, metaResult{
		DBPath: s.opts.DBPath, Root: s.opts.Root, SchemaVersion: v,
	}, nil)
}

// ---------------------------------------------------------------------------
// doctor

type doctorInput struct{}

func (s *Server) opDoctor(ctx context.Context, _ *doctorInput) (*body, error) {
	env := &doctor.Env{Store: s.opts.Store, DBPath: s.opts.DBPath, Root: s.opts.Root}
	report, err := doctor.Run(ctx, env, doctor.DefaultChecks())
	if err != nil {
		return nil, huma.Error500InternalServerError("run doctor", err)
	}
	return s.wrap("api.doctor", nil, report, nil)
}

// ---------------------------------------------------------------------------
// features

type featureView struct {
	ID    shared.FeatureID `json:"id"`
	Name  string           `json:"name,omitempty"`
	Kind  string           `json:"kind,omitempty"`
	Owner string           `json:"owner,omitempty"`
}

type featuresInput struct{}

func (s *Server) opFeatures(ctx context.Context, _ *featuresInput) (*body, error) {
	feats, err := s.opts.Store.Features().List(ctx, store.FeatureFilter{})
	if err != nil {
		return nil, huma.Error500InternalServerError("list features", err)
	}
	out := make([]featureView, 0, len(feats))
	for _, f := range feats {
		out = append(out, viewOf(f))
	}
	// Sorted: an unordered list differs from itself between requests, which
	// is the same defect --stable exists to remove.
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return s.wrap("api.features", map[string]any{"count": len(out)}, out, nil)
}

type featureInput struct {
	ID string `path:"id" doc:"feature id, e.g. billing.pay"`
}

func (s *Server) opFeature(ctx context.Context, in *featureInput) (*body, error) {
	f, err := s.opts.Store.Features().Get(ctx, shared.FeatureID(in.ID))
	if err != nil {
		// Both sentinels: the features port answers with the specific one and
		// shared.ErrNotFound is the generic contract other ports use. Mapping
		// only one turns a missing feature into a 500, which tells a consumer
		// the server is broken rather than that they asked for something that
		// is not there.
		if errors.Is(err, shared.ErrFeatureNotFound) || errors.Is(err, shared.ErrNotFound) {
			return nil, huma.Error404NotFound(fmt.Sprintf("no feature %q in this index", in.ID))
		}
		return nil, huma.Error500InternalServerError("get feature", err)
	}
	return s.wrap("api.feature", map[string]any{"id": in.ID}, viewOf(f), nil)
}

func viewOf(f store.Feature) featureView {
	v := featureView{ID: f.ID, Name: f.Title, Kind: string(f.Kind)}
	if f.Owner != nil {
		v.Owner = *f.Owner
	}
	return v
}

// ---------------------------------------------------------------------------
// graph

type graphView struct {
	Nodes []nodeView `json:"nodes"`
	Edges []edgeView `json:"edges"`
}

type nodeView struct {
	ID   shared.SymbolID `json:"id"`
	Kind string          `json:"kind,omitempty"`
	Path string          `json:"path,omitempty"`
	Line int             `json:"line,omitempty"`
}

type edgeView struct {
	From shared.SymbolID `json:"from"`
	To   shared.SymbolID `json:"to"`
	Kind string          `json:"kind,omitempty"`
	// Tier is never omitempty. An edge that does not state how it was
	// resolved reads as one atlas verified, and packages/store's EdgeRow
	// makes the same choice for the same reason.
	Tier      graph.ResolutionTier `json:"tier"`
	Ambiguous bool                 `json:"ambiguous,omitempty"`
}

type graphInput struct {
	ID string `path:"id" doc:"feature id whose neighbourhood to return"`
}

// opGraph returns one feature's neighbourhood.
//
// Feature-scoped rather than whole-graph, as #101 specifies. That is not only
// a scoping choice: the store has no whole-graph edge read, because edges are
// reached through Out(fromID)/In(toID), and adding one to serve a view nobody
// asked for would be a schema change made for a convenience.
func (s *Server) opGraph(ctx context.Context, in *graphInput) (*body, error) {
	id := shared.FeatureID(in.ID)
	links, err := s.opts.Store.FeatureSymbols().ListByFeature(ctx, id)
	if err != nil {
		return nil, huma.Error500InternalServerError("list feature symbols", err)
	}
	out := graphView{Nodes: []nodeView{}, Edges: []edgeView{}}
	if len(links) == 0 {
		// A feature with no linked symbols is a real answer -- it is what
		// doctor's feature.linkage check reports on -- so it is an empty
		// graph, not a 404.
		return s.wrap("api.graph", map[string]any{"feature": in.ID}, out, nil)
	}

	// One read of the symbol table, indexed by surrogate id. The store has no
	// FindByID, and a lookup per link would be a query per symbol.
	rows, err := s.opts.Store.Symbols().List(ctx, store.SymbolFilter{})
	if err != nil {
		return nil, huma.Error500InternalServerError("list symbols", err)
	}
	byID := make(map[int64]store.SymbolRow, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
	}

	seen := map[int64]bool{}
	addNode := func(sid int64) {
		if seen[sid] {
			return
		}
		seen[sid] = true
		if row, ok := byID[sid]; ok {
			out.Nodes = append(out.Nodes, nodeView{
				ID: row.QualifiedName, Kind: string(row.Kind),
				Path: row.FilePath, Line: row.Line,
			})
		}
	}

	for _, l := range links {
		addNode(l.SymbolID)
		edges, eerr := s.opts.Store.Edges().Out(ctx, l.SymbolID)
		if eerr != nil {
			return nil, huma.Error500InternalServerError("read edges", eerr)
		}
		for _, e := range edges {
			from, fromOK := byID[e.FromID]
			to, toOK := byID[e.ToID]
			if !fromOK || !toOK {
				continue
			}
			addNode(e.FromID)
			addNode(e.ToID)
			out.Edges = append(out.Edges, edgeView{
				From: from.QualifiedName, To: to.QualifiedName,
				Kind: string(e.Kind), Tier: e.Tier, Ambiguous: e.Ambiguous,
			})
		}
	}

	sort.Slice(out.Nodes, func(i, j int) bool { return out.Nodes[i].ID < out.Nodes[j].ID })
	sort.Slice(out.Edges, func(i, j int) bool {
		if out.Edges[i].From != out.Edges[j].From {
			return out.Edges[i].From < out.Edges[j].From
		}
		return out.Edges[i].To < out.Edges[j].To
	})
	return s.wrap("api.graph",
		map[string]any{"feature": in.ID, "nodes": len(out.Nodes), "edges": len(out.Edges)},
		out, nil)
}
