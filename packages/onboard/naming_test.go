package onboard

import "testing"

// A provisional id must always be promotable. shared.ValidFeatureIDRe demands
// at least two dot-separated lowercase segments, so a naming rule that can
// emit a bare word produces proposals that cannot be accepted -- and the
// failure would only surface at promote time, on the user's repository.
func TestCapabilityIDFromDir(t *testing.T) {
	cases := []struct {
		dir      string
		wantID   string
		wantName string
	}{
		{"packages/store", "packages.store", "store"},
		{"packages/codeindex/go", "codeindex.go", "go"},
		{"internal/cli", "internal.cli", "cli"},
		{"services/billing/checkout", "billing.checkout", "checkout"},
		{"cmd", "root.cmd", "cmd"},
		{"", "root.root", "root"},
		{"src/Feature Flags", "src.feature-flags", "feature-flags"},
	}
	for _, c := range cases {
		gotID, gotName := capabilityIDFromDir(c.dir)
		if gotID != c.wantID || gotName != c.wantName {
			t.Errorf("capabilityIDFromDir(%q) = (%q,%q), want (%q,%q)",
				c.dir, gotID, gotName, c.wantID, c.wantName)
		}
		if !validID(gotID) {
			t.Errorf("capabilityIDFromDir(%q) = %q which is not a promotable feature id", c.dir, gotID)
		}
	}
}

func TestCapabilityIDFromRoute(t *testing.T) {
	cases := []struct {
		method, path string
		want         string
	}{
		{"POST", "/measurements", "measurements.create"},
		{"GET", "/measurements", "measurements.list"},
		{"GET", "/api/v1/measurements/{id}", "measurements.read"},
		{"GET", "/api/v1/users/:id/sessions", "sessions.list"},
		{"DELETE", "/users/{id}", "users.delete"},
		{"PATCH", "/users/{id}", "users.update"},
		{"PUT", "/users/{id}", "users.replace"},
		{"GET", "/", "root.list"},
		// An unrecognised verb must still yield a promotable id rather than
		// a half-formed one; the method itself is the honest name for it.
		{"TRACE", "/debug", "debug.trace"},
		// A registration with no method serves every method. "handle" says
		// that; the empty string would collapse the id to one segment and
		// make it unpromotable.
		{"", "/sprint", "sprint.handle"},
	}
	for _, c := range cases {
		got := capabilityIDFromRoute(c.method, c.path)
		if got != c.want {
			t.Errorf("capabilityIDFromRoute(%q,%q) = %q, want %q", c.method, c.path, got, c.want)
		}
		if !validID(got) {
			t.Errorf("route id %q is not a promotable feature id", got)
		}
	}
}

func TestTestNameCluster(t *testing.T) {
	cases := []struct {
		fn   string
		want string
	}{
		{"TestCheckoutIdempotent", "checkout"},
		{"TestCheckout", "checkout"},
		{"Test_Checkout_Idempotent", "checkout"},
		{"TestHTTPRouteParsing", "httproute"},
		// Stop words name no capability: TestNewStore is about a
		// constructor, not about a capability called "new".
		{"TestNewStore", ""},
		{"TestGetUser", ""},
		{"Benchmark_Something", ""},
		{"NotATest", ""},
		{"Test", ""},
	}
	for _, c := range cases {
		if got := testNameCluster(c.fn); got != c.want {
			t.Errorf("testNameCluster(%q) = %q, want %q", c.fn, got, c.want)
		}
	}
}
