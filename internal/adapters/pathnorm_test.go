package adapters

import "testing"

// The tests in this file exist because the Windows CI leg (issue #143)
// found a whole class of bug that a Linux-only suite structurally cannot
// see: helpers that split a path on "/" and are handed a path built with
// "\". Every case below therefore states BOTH separators explicitly
// rather than deriving one from the host — a test that passes only
// because the runner is Linux is exactly what let this rot for months.

func TestSlashPath_NormalisesEitherSeparator(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"posix untouched", "tests/e2e/auth/login.spec.ts", "tests/e2e/auth/login.spec.ts"},
		{"windows converted", `tests\e2e\auth\login.spec.ts`, "tests/e2e/auth/login.spec.ts"},
		{"mixed", `tests\e2e/auth\login.spec.ts`, "tests/e2e/auth/login.spec.ts"},
		{"drive letter kept", `C:\repo\e2e\auth.spec.ts`, "C:/repo/e2e/auth.spec.ts"},
		{"bare file", "auth.spec.ts", "auth.spec.ts"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := slashPath(tt.in); got != tt.want {
				t.Errorf("slashPath(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestInferFeatureFromPath_SeparatorAgnostic pins the property the
// Playwright report parser actually needs: the feature id depends on the
// path's segments, never on which OS wrote the separator between them.
//
// Both forms are asserted on every host because a Playwright JSON report
// is a portable artifact — it can be produced on a Windows runner and
// parsed on a Linux one, so the separator is a property of the file, not
// of the process reading it.
func TestInferFeatureFromPath_SeparatorAgnostic(t *testing.T) {
	t.Parallel()
	tests := []struct {
		posix   string
		windows string
		want    string
	}{
		{"e2e/auth.spec.ts", `e2e\auth.spec.ts`, "auth"},
		{"e2e/meals/log.spec.ts", `e2e\meals\log.spec.ts`, "meals.log"},
		{"tests/e2e/auth/login.spec.ts", `tests\e2e\auth\login.spec.ts`, "auth.login"},
		{"specs/profile.spec.ts", `specs\profile.spec.ts`, "profile"},
		{"apps/web/e2e/Billing/Invoice.spec.js", `apps\web\e2e\Billing\Invoice.spec.js`, "billing.invoice"},
		// No directory context at all: the base name is the whole answer.
		{"checkout.spec.ts", "checkout.spec.ts", "checkout"},
	}
	for _, tt := range tests {
		t.Run(tt.posix, func(t *testing.T) {
			t.Parallel()
			if got := inferFeatureFromPath(tt.posix); got != tt.want {
				t.Errorf("inferFeatureFromPath(%q) = %q, want %q", tt.posix, got, tt.want)
			}
			if got := inferFeatureFromPath(tt.windows); got != tt.want {
				t.Errorf("inferFeatureFromPath(%q) = %q, want %q", tt.windows, got, tt.want)
			}
		})
	}
}

// TestShortFileName_SeparatorAgnostic covers the same latent bug in the
// audit renderer: shortFileName split on "/" only, so a Windows-produced
// path rendered as the entire path instead of the base name.
func TestShortFileName_SeparatorAgnostic(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want string
	}{
		{"internal/adapters/audit_renderer.go", "audit_renderer.go"},
		{`internal\adapters\audit_renderer.go`, "audit_renderer.go"},
		{`C:\repo\internal\adapters\audit_renderer.go`, "audit_renderer.go"},
		{"audit_renderer.go", "audit_renderer.go"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			if got := shortFileName(tt.in); got != tt.want {
				t.Errorf("shortFileName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
