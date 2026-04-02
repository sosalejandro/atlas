// @testreg server.http-handlers
package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sosalejandro/testreg/internal/adapters"
)

// testdataDir returns the absolute path to testdata/registry.
// It uses runtime.Caller so the path is correct regardless of where
// `go test` is invoked from.
func testdataDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "testdata", "registry")
}

// newTestServer creates a Server backed by the fixture registry and a
// StubGraphBuilder (no real AST scanning during tests).
func newTestServer(t *testing.T) *Server {
	t.Helper()
	regDir := testdataDir(t)
	projectRoot := filepath.Dir(filepath.Dir(regDir)) // testdata/

	srv, err := NewForTesting(regDir, projectRoot, "test-project", adapters.NewStubGraphBuilder())
	if err != nil {
		t.Fatalf("NewForTesting: %v", err)
	}
	return srv
}

// get fires a GET request and returns the recorder.
func get(t *testing.T, srv *Server, path string, headers ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// post fires a POST request with form values and returns the recorder.
func post(t *testing.T, srv *Server, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func assertStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		body := rec.Body.String()
		t.Errorf("status = %d, want %d\nbody: %s", rec.Code, want, body[:min(200, len(body))])
	}
}

func assertContains(t *testing.T, rec *httptest.ResponseRecorder, substr string) {
	t.Helper()
	if !strings.Contains(rec.Body.String(), substr) {
		body := rec.Body.String()
		t.Errorf("response body does not contain %q\npreview: %s", substr, body[:min(400, len(body))])
	}
}

func assertNotContains(t *testing.T, rec *httptest.ResponseRecorder, substr string) {
	t.Helper()
	if strings.Contains(rec.Body.String(), substr) {
		t.Errorf("response body should NOT contain %q", substr)
	}
}


// ─── Full page routes ─────────────────────────────────────────────────────────

func TestFullPageRoutes_Return200WithShell(t *testing.T) {
	srv := newTestServer(t)

	routes := []string{
		"/",
		"/features",
		"/graph",
		"/sprint",
		"/contract",
		"/metrics",
		"/diff",
		"/diagnose",
	}

	for _, path := range routes {
		t.Run(path, func(t *testing.T) {
			rec := get(t, srv, path)
			assertStatus(t, rec, http.StatusOK)
			// Full page must include the HTML shell markers.
			assertContains(t, rec, `id="page-content"`)
			assertContains(t, rec, `id="status-bar"`)
			// And the nav / modal container from the shell.
			assertContains(t, rec, `id="modal-container"`)
		})
	}
}

func TestFullPageRoot_404OnUnknownPath(t *testing.T) {
	srv := newTestServer(t)
	rec := get(t, srv, "/nonexistent-path")
	assertStatus(t, rec, http.StatusNotFound)
}

// ─── Partial routes (htmx swaps) ─────────────────────────────────────────────

func TestPartialRoutes_ReturnContentWithoutShell(t *testing.T) {
	srv := newTestServer(t)

	routes := []string{
		"/pages/overview",
		"/pages/features",
		"/pages/graph",
		"/pages/sprint",
		"/pages/contract",
		"/pages/metrics",
		"/pages/diff",
		"/pages/diagnose",
	}

	for _, path := range routes {
		t.Run(path, func(t *testing.T) {
			rec := get(t, srv, path)
			assertStatus(t, rec, http.StatusOK)
			// Partials must NOT include the full HTML shell.
			assertNotContains(t, rec, `<html`)
			assertNotContains(t, rec, `id="sidebar"`)
		})
	}
}

// ─── Features page ────────────────────────────────────────────────────────────

func TestFeaturesPage_ShowsAllFeatures(t *testing.T) {
	srv := newTestServer(t)
	rec := get(t, srv, "/features")
	assertStatus(t, rec, http.StatusOK)
	assertContains(t, rec, "auth.login")
	assertContains(t, rec, "billing.checkout")
}

func TestFeaturesPartial_FilterByQuery(t *testing.T) {
	srv := newTestServer(t)
	rec := get(t, srv, "/pages/features?q=auth")
	assertStatus(t, rec, http.StatusOK)
	assertContains(t, rec, "auth.login")
	assertNotContains(t, rec, "billing.checkout")
}

func TestFeaturesPartial_FilterByPriority(t *testing.T) {
	srv := newTestServer(t)
	rec := get(t, srv, "/pages/features?priority=critical")
	assertStatus(t, rec, http.StatusOK)
	assertContains(t, rec, "auth.login")
	// billing.checkout is critical, should also appear.
	assertContains(t, rec, "billing.checkout")
	// billing.refund is medium, should not appear.
	assertNotContains(t, rec, "billing.refund")
}

func TestFeaturesPartial_HXTargetTableBody_ReturnsRowsOnly(t *testing.T) {
	srv := newTestServer(t)
	rec := get(t, srv, "/pages/features?q=auth",
		"HX-Target", "feature-table-body")
	assertStatus(t, rec, http.StatusOK)
	// Only rows, no page wrapper.
	assertNotContains(t, rec, `Features Analysis`)
	assertContains(t, rec, "auth.login")
}

// ─── Graph page ───────────────────────────────────────────────────────────────

func TestGraphPage_DefaultsToFirstFeature(t *testing.T) {
	srv := newTestServer(t)
	rec := get(t, srv, "/graph")
	assertStatus(t, rec, http.StatusOK)
	assertContains(t, rec, "Dependency Graph")
}

func TestGraphPage_SpecificFeature(t *testing.T) {
	srv := newTestServer(t)
	rec := get(t, srv, "/graph?feature=auth.login")
	assertStatus(t, rec, http.StatusOK)
	assertContains(t, rec, "auth.login")
}

// ─── Contract page ────────────────────────────────────────────────────────────

func TestContractPartial_KnownFeature(t *testing.T) {
	srv := newTestServer(t)
	rec := get(t, srv, "/pages/contract?feature=auth.login")
	assertStatus(t, rec, http.StatusOK)
	assertContains(t, rec, "auth.login")
}

// ─── Diagnose page ────────────────────────────────────────────────────────────

func TestDiagnosePartial_GET(t *testing.T) {
	srv := newTestServer(t)
	rec := get(t, srv, "/pages/diagnose")
	assertStatus(t, rec, http.StatusOK)
	assertContains(t, rec, "Symptom")
}

func TestDiagnosePartial_POST_WithSymptom(t *testing.T) {
	srv := newTestServer(t)
	form := url.Values{
		"feature": {"auth.login"},
		"symptom": {"401"},
	}
	rec := post(t, srv, "/pages/diagnose", form)
	assertStatus(t, rec, http.StatusOK)
	// Should render the diagnose-result template (with or without a match).
	body := rec.Body.String()
	hasResult := strings.Contains(body, "Best Match") ||
		strings.Contains(body, "No matching diagnostic")
	if !hasResult {
		t.Errorf("expected diagnose result content, got: %s", body[:min(300, len(body))])
	}
}

func TestDiagnosePartial_POST_EmptySymptom(t *testing.T) {
	srv := newTestServer(t)
	form := url.Values{
		"feature": {"auth.login"},
		"symptom": {""},
	}
	rec := post(t, srv, "/pages/diagnose", form)
	assertStatus(t, rec, http.StatusOK)
}

// ─── API: Feature detail panel ────────────────────────────────────────────────

func TestFeatureDetail_ValidFeature(t *testing.T) {
	srv := newTestServer(t)
	rec := get(t, srv, "/api/feature/auth.login")
	assertStatus(t, rec, http.StatusOK)
	assertContains(t, rec, "auth.login")
	assertContains(t, rec, "feature-detail-panel")
}

func TestFeatureDetail_UnknownFeature(t *testing.T) {
	srv := newTestServer(t)
	rec := get(t, srv, "/api/feature/does.not.exist")
	assertStatus(t, rec, http.StatusOK)
	// Should render the error variant, not 500.
	assertContains(t, rec, "feature-detail-panel")
}

func TestFeatureDetail_MissingID(t *testing.T) {
	srv := newTestServer(t)
	rec := get(t, srv, "/api/feature/")
	assertStatus(t, rec, http.StatusBadRequest)
}

// ─── API: Scan modal ──────────────────────────────────────────────────────────

func TestScanModal_ReturnsModalHTML(t *testing.T) {
	srv := newTestServer(t)
	rec := get(t, srv, "/pages/scan-modal")
	assertStatus(t, rec, http.StatusOK)
	assertContains(t, rec, "scan-modal-overlay")
	assertContains(t, rec, "Run Scan")
	assertNotContains(t, rec, `<html`)
}

// ─── API: Scan ────────────────────────────────────────────────────────────────

func TestScan_MethodNotAllowed(t *testing.T) {
	srv := newTestServer(t)
	rec := get(t, srv, "/api/scan")
	assertStatus(t, rec, http.StatusMethodNotAllowed)
}

func TestScan_POST_ReturnsStatusBar(t *testing.T) {
	srv := newTestServer(t)
	rec := post(t, srv, "/api/scan", url.Values{})
	assertStatus(t, rec, http.StatusOK)
	assertContains(t, rec, "status-bar")
}

// ─── API: Diff ────────────────────────────────────────────────────────────────

func TestDiffSnapshot_POST_SavesAndReturnsContent(t *testing.T) {
	srv := newTestServer(t)
	// Override snapshot dir to temp so we don't pollute testdata.
	srv.projectRoot = t.TempDir()

	form := url.Values{"name": {"test-snap-1"}}
	rec := post(t, srv, "/api/diff/snapshot", form)
	assertStatus(t, rec, http.StatusOK)
	// Should return the refreshed diff page content.
	assertContains(t, rec, "test-snap-1")
}

func TestDiffCompare_AfterSnapshot(t *testing.T) {
	srv := newTestServer(t)
	srv.projectRoot = t.TempDir()

	// Save a baseline first.
	if err := srv.saveSnapshot("baseline"); err != nil {
		t.Fatalf("saveSnapshot: %v", err)
	}

	rec := get(t, srv, "/api/diff/compare?from=baseline&to=current")
	assertStatus(t, rec, http.StatusOK)
	// Result table or empty (all unchanged since it's the same registry).
	body := rec.Body.String()
	_ = body // no panic, no 500 is sufficient
}

func TestDiffCompare_MissingSnapshot_Returns500(t *testing.T) {
	srv := newTestServer(t)
	srv.projectRoot = t.TempDir()

	rec := get(t, srv, "/api/diff/compare?from=nonexistent&to=current")
	assertStatus(t, rec, http.StatusInternalServerError)
}

// ─── Sprint page ──────────────────────────────────────────────────────────────

func TestSprintPartial_ShowsPrioritizedItems(t *testing.T) {
	srv := newTestServer(t)
	rec := get(t, srv, "/pages/sprint")
	assertStatus(t, rec, http.StatusOK)
	// auth.refresh and billing.checkout are missing coverage — should appear.
	body := rec.Body.String()
	hasSprint := strings.Contains(body, "auth.refresh") ||
		strings.Contains(body, "billing.checkout") ||
		strings.Contains(body, "All features are at or above target")
	if !hasSprint {
		t.Errorf("expected sprint content, got: %s", body[:min(300, len(body))])
	}
}

// ─── Metrics page ─────────────────────────────────────────────────────────────

func TestMetricsPage_ShowsCoverageBars(t *testing.T) {
	srv := newTestServer(t)
	rec := get(t, srv, "/pages/metrics")
	assertStatus(t, rec, http.StatusOK)
	assertContains(t, rec, "Unit Tests")
	assertContains(t, rec, "Integration Tests")
}

// ─── Status bar reflects real registry data ───────────────────────────────────

func TestOverviewPage_StatusBarHasFeatureCount(t *testing.T) {
	srv := newTestServer(t)
	rec := get(t, srv, "/")
	assertStatus(t, rec, http.StatusOK)
	// Fixture has 5 features total (3 auth + 2 billing).
	assertContains(t, rec, "5")
}
