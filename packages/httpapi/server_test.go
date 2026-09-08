package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sosalejandro/atlas/packages/envelope"
	"github.com/sosalejandro/atlas/packages/httpapi"
	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

func newServer(t *testing.T, stable bool) (*httpapi.Server, *store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "atlas.db")
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	srv, err := httpapi.New(httpapi.Options{
		Store: s, Root: dir, DBPath: dbPath, Stable: stable,
		// Pinned so a non-stable envelope is still reproducible in tests;
		// the point of --stable is proved by the field being ABSENT, not by
		// the clock standing still.
		Now: func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
	})
	if err != nil {
		t.Fatalf("httpapi.New: %v", err)
	}
	return srv, s, dir
}

func getRaw(t *testing.T, srv *httpapi.Server, path string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func get(t *testing.T, srv *httpapi.Server, path string) (int, envelope.Envelope, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()
	var env envelope.Envelope
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("GET %s: body is not an envelope: %v\n%s", path, err, body)
	}
	return rec.Code, env, body
}

// THE security property. The API has no authentication and serves a complete
// map of somebody's source; a routable bind publishes that to the network.
// The refusal lives in the server so every caller inherits it.
func TestRequireLoopback_RefusesAnythingRoutable(t *testing.T) {
	refused := []string{
		"0.0.0.0:7777",      // every interface -- the usual "make it reachable"
		"192.168.1.10:7777", // a LAN address
		"8.8.8.8:7777",      // routable
		"[::]:7777",         // every interface, v6
		":7777",             // empty host: every interface
		"example.com:7777",  // a name that is not localhost
	}
	for _, addr := range refused {
		t.Run(addr, func(t *testing.T) {
			if err := httpapi.RequireLoopback(addr); err == nil {
				t.Errorf("bound %q, which publishes the index to the network", addr)
			}
		})
	}
	for _, addr := range []string{"127.0.0.1:7777", "[::1]:7777", "localhost:7777", "127.0.0.1:0"} {
		t.Run(addr, func(t *testing.T) {
			if err := httpapi.RequireLoopback(addr); err != nil {
				t.Errorf("refused loopback %q: %v", addr, err)
			}
		})
	}
}

func TestAPI_AnswersInTheSameEnvelopeAsTheCLI(t *testing.T) {
	srv, _, _ := newServer(t, false)
	code, env, body := get(t, srv, "/api/meta")
	if code != http.StatusOK {
		t.Fatalf("status = %d: %s", code, body)
	}
	if env.SchemaVersion != envelope.SchemaVersion {
		t.Errorf("schema_version = %q, want %q", env.SchemaVersion, envelope.SchemaVersion)
	}
	if env.Command != "api.meta" {
		t.Errorf("command = %q, want api.meta", env.Command)
	}
	if env.GeneratedAt == "" {
		t.Error("no generated_at outside --stable")
	}
}

// An error is an envelope too, so a consumer parses one shape whatever
// happened.
func TestAPI_ErrorsAreEnvelopesWithTheRightStatus(t *testing.T) {
	srv, _, _ := newServer(t, false)

	// Errors are RFC 7807 problem+json, huma's contract, described in the
	// generated OpenAPI document -- a different machine-readable shape from
	// the success envelope, not an unstructured one.
	code, body := getRaw(t, srv, "/api/features/does.not.exist")
	if code != http.StatusNotFound {
		t.Errorf("missing feature status = %d, want 404: %s", code, body)
	}
	var problem map[string]any
	if err := json.Unmarshal([]byte(body), &problem); err != nil {
		t.Fatalf("error body is not JSON: %v\n%s", err, body)
	}
	if problem["status"] != float64(http.StatusNotFound) {
		t.Errorf("problem document does not carry the status: %s", body)
	}
	if !strings.Contains(body, "does.not.exist") {
		t.Errorf("the error does not name what was missing: %s", body)
	}
}

// doctor over HTTP must reach the same verdict as doctor on the CLI, or a
// consumer gating on one disagrees with a consumer gating on the other about
// whether the index can be trusted at all.
func TestAPI_DoctorRunsTheSameCheckSet(t *testing.T) {
	srv, _, _ := newServer(t, false)
	code, env, body := get(t, srv, "/api/doctor")
	if code != http.StatusOK {
		t.Fatalf("status = %d: %s", code, body)
	}
	result, ok := env.Result.(map[string]any)
	if !ok {
		t.Fatalf("result is %T", env.Result)
	}
	checks, ok := result["checks"].([]any)
	if !ok || len(checks) == 0 {
		t.Fatalf("no checks in the report; a clean report over nothing is not a clean report: %s", body)
	}
	for _, want := range []string{"index.freshness", "store.schema"} {
		if !strings.Contains(body, want) {
			t.Errorf("the HTTP report is missing %q, which the CLI reports", want)
		}
	}
}

func TestAPI_FeaturesAreListedAndFetchable(t *testing.T) {
	srv, s, _ := newServer(t, false)
	ctx := context.Background()
	for _, id := range []shared.FeatureID{"billing.pay", "auth.login"} {
		if err := s.Features().Upsert(ctx, store.Feature{
			ID: id, Title: string(id), Kind: store.FeatureKindFeature,
		}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}

	_, env, body := get(t, srv, "/api/features")
	list, ok := env.Result.([]any)
	if !ok {
		t.Fatalf("result is %T: %s", env.Result, body)
	}
	if len(list) != 2 {
		t.Fatalf("got %d features, want 2: %s", len(list), body)
	}
	// Sorted: an unordered list differs from itself between requests, which
	// is the same defect --stable exists to remove.
	first := list[0].(map[string]any)["id"]
	if first != "auth.login" {
		t.Errorf("features are not sorted by id; first = %v", first)
	}

	code, env, body := get(t, srv, "/api/features/billing.pay")
	if code != http.StatusOK {
		t.Fatalf("status = %d: %s", code, body)
	}
	if env.Result.(map[string]any)["id"] != "billing.pay" {
		t.Errorf("wrong feature returned: %s", body)
	}
}

// A feature with no linked symbols is a real answer, not a 404 -- it is
// exactly what doctor's feature.linkage check reports on.
func TestAPI_GraphOfAnUnlinkedFeatureIsEmptyNotMissing(t *testing.T) {
	srv, s, _ := newServer(t, false)
	if err := s.Features().Upsert(context.Background(), store.Feature{
		ID: "billing.pay", Title: "Pay", Kind: store.FeatureKindFeature,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	code, env, body := get(t, srv, "/api/features/billing.pay/graph")
	if code != http.StatusOK {
		t.Fatalf("status = %d: %s", code, body)
	}
	g := env.Result.(map[string]any)
	if len(g["nodes"].([]any)) != 0 || len(g["edges"].([]any)) != 0 {
		t.Errorf("expected an empty graph: %s", body)
	}
}

// The same contract as the CLI's --stable, on the same policy, because the
// desktop app will want to diff two answers exactly as CI does.
func TestAPI_StableDropsTheEnvelopeStamp(t *testing.T) {
	srv, _, _ := newServer(t, true)
	_, env, body := get(t, srv, "/api/doctor")
	if env.GeneratedAt != "" {
		t.Errorf("--stable envelope carries generated_at = %q", env.GeneratedAt)
	}
	var tree any
	if err := json.Unmarshal([]byte(body), &tree); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := envelope.VolatileKeysIn(tree); len(got) != 0 {
		t.Errorf("stable response still carries volatile keys: %v", got)
	}
}

func TestServer_ListenRefusesRoutableAddresses(t *testing.T) {
	srv, _, _ := newServer(t, false)
	err := srv.Listen(context.Background(), "0.0.0.0:0")
	if err == nil {
		t.Fatal("Listen bound 0.0.0.0")
	}
	if !strings.Contains(err.Error(), "loopback") {
		t.Errorf("error does not explain the refusal: %v", err)
	}
}

// The claim this package exists to make true: the HTTP surface answers in the
// SAME envelope as `atlas --json`. A field on one and not the other is the
// drift the testreg dashboard died of, in miniature -- huma adds a `$schema`
// link by default, and this is what notices if it ever comes back.
func TestAPI_EnvelopeHasNoFieldsTheCLIDoesNot(t *testing.T) {
	srv, _, _ := newServer(t, false)
	_, _, body := get(t, srv, "/api/meta")

	var got map[string]any
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// The envelope's own fields, from packages/envelope. Anything else in a
	// response is a field the CLI does not emit.
	allowed := map[string]bool{
		"schema_version": true, "command": true, "args": true,
		"result": true, "warnings": true, "generated_at": true,
	}
	for k := range got {
		if !allowed[k] {
			t.Errorf("the API emits %q, which `atlas --json` does not; the two "+
				"surfaces have started to drift", k)
		}
	}
	if _, ok := got["schema_version"]; !ok {
		t.Error("no schema_version; the comparison above would pass on an empty object")
	}
}
