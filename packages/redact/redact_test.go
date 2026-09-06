package redact

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestText_LeavesCleanTextByteIdentical(t *testing.T) {
	const in = "SELECT id, email FROM users WHERE tenant_id = $1 ORDER BY id LIMIT 50"
	got := Text(in)
	if got.Text != in {
		t.Errorf("Text rewrote clean input:\n got %q\nwant %q", got.Text, in)
	}
	if got.Redacted() {
		t.Errorf("Redacted() = true for clean input, findings %+v", got.Findings)
	}
	if len(got.Findings) != 0 {
		t.Errorf("Findings = %+v, want none", got.Findings)
	}
}

func TestText_ReplacesTheSecretAndKeepsEverythingElse(t *testing.T) {
	const in = `dsn := "postgres://svc_orders:Kq9Xm2Vz7Pw4Rt6Y@db.internal:5432/orders"`
	got := Text(in)
	if !got.Redacted() {
		t.Fatalf("Redacted() = false, want true (findings %+v)", got.Findings)
	}
	if strings.Contains(got.Text, "Kq9Xm2Vz7Pw4Rt6Y") {
		t.Fatalf("redacted text still contains the password: %q", got.Text)
	}
	// The parts that make the row worth analysing must survive: which
	// database, which user, which host, which port, which schema.
	for _, keep := range []string{"postgres://", "svc_orders", "db.internal", "5432", "orders"} {
		if !strings.Contains(got.Text, keep) {
			t.Errorf("redaction destroyed %q: %q", keep, got.Text)
		}
	}
	want := Placeholder(KindConnectionString, got.Findings[0].Digest)
	if !strings.Contains(got.Text, want) {
		t.Errorf("redacted text %q does not contain placeholder %q", got.Text, want)
	}
}

func TestText_ReplacesEverySecretWhenSeveralArePresent(t *testing.T) {
	in := "AKIAIOSFODNN7EXAMPLE\n" +
		`apiKey = "` + stripeShaped + `"` + "\n" +
		"trailing text\n"
	got := Text(in)
	if len(got.Findings) != 2 {
		t.Fatalf("Findings = %+v, want 2", got.Findings)
	}
	for _, secret := range []string{"AKIAIOSFODNN7EXAMPLE", stripeShaped} {
		if strings.Contains(got.Text, secret) {
			t.Errorf("secret %q survived redaction: %q", secret, got.Text)
		}
	}
	if !strings.Contains(got.Text, "apiKey = ") {
		t.Errorf("redaction ate the assignment it was anchored on: %q", got.Text)
	}
	if !strings.HasSuffix(got.Text, "trailing text\n") {
		t.Errorf("redaction truncated the tail: %q", got.Text)
	}
}

func TestText_IsIdempotent(t *testing.T) {
	// Re-running redaction over an already-redacted store must be a no-op.
	// If a placeholder could itself match a detector, every sweep would
	// rewrite the previous sweep's output and the digests -- the only thing
	// tying nine occurrences of one leaked credential together -- would
	// change on every run.
	inputs := []string{
		`apiKey = "` + stripeShaped + `"`,
		`dsn := "postgres://svc:Kq9Xm2Vz7Pw4Rt6Y@db.internal:5432/orders"`,
		"AKIAIOSFODNN7EXAMPLE",
		"-----BEGIN RSA PRIVATE KEY-----\nQUJDREVGRw==\n-----END RSA PRIVATE KEY-----",
	}
	for _, in := range inputs {
		once := Text(in)
		if !once.Redacted() {
			t.Fatalf("Text(%q) found nothing to redact", in)
		}
		twice := Text(once.Text)
		if twice.Redacted() {
			t.Errorf("re-scanning redacted text %q found %+v; placeholders must be inert",
				once.Text, twice.Findings)
		}
		if twice.Text != once.Text {
			t.Errorf("second pass changed the text:\n first %q\nsecond %q", once.Text, twice.Text)
		}
	}
}

func TestPlaceholder_CarriesKindAndDigestOnly(t *testing.T) {
	got := Placeholder(KindAssignedSecret, "abc123def456")
	if !strings.Contains(got, string(KindAssignedSecret)) {
		t.Errorf("placeholder %q does not name the kind", got)
	}
	if !strings.Contains(got, "abc123def456") {
		t.Errorf("placeholder %q does not carry the digest", got)
	}
	if Scan(got) != nil {
		t.Errorf("the placeholder itself trips a detector: %+v", Scan(got))
	}
	if !IsPlaceholder(got) {
		t.Errorf("IsPlaceholder(%q) = false", got)
	}
	for _, notPlaceholder := range []string{
		"", "SELECT 1", "[redacted]", "prefix " + got, got + " suffix",
	} {
		if IsPlaceholder(notPlaceholder) {
			t.Errorf("IsPlaceholder(%q) = true", notPlaceholder)
		}
	}
}

// TestText_LeavesASnapshotBlobParseable is the reason the value pattern
// refuses to swallow backslashes. snapshots.index_json is JSON; a redaction
// that ate one half of an escape pair would leave `atlas diff` unable to
// decode the snapshot it just cleaned.
func TestText_LeavesASnapshotBlobParseable(t *testing.T) {
	blob := `{"symbols":[{"id":"cfg.Load",` +
		`"doc":"apiKey = \"` + stripeShaped + `\" is the fallback"}]}`
	if !json.Valid([]byte(blob)) {
		t.Fatalf("the fixture is not valid JSON to begin with: %s", blob)
	}
	got := Text(blob)
	if !got.Redacted() {
		t.Fatalf("no finding in %s", blob)
	}
	if strings.Contains(got.Text, stripeShaped) {
		t.Fatalf("the key survived: %s", got.Text)
	}
	if !json.Valid([]byte(got.Text)) {
		t.Fatalf("redaction produced invalid JSON: %s", got.Text)
	}
	var decoded struct {
		Symbols []struct {
			ID  string `json:"id"`
			Doc string `json:"doc"`
		} `json:"symbols"`
	}
	if err := json.Unmarshal([]byte(got.Text), &decoded); err != nil {
		t.Fatalf("decode redacted blob: %v", err)
	}
	if len(decoded.Symbols) != 1 || decoded.Symbols[0].ID != "cfg.Load" {
		t.Fatalf("redaction changed the structure: %+v", decoded)
	}
	if !strings.HasSuffix(decoded.Symbols[0].Doc, " is the fallback") {
		t.Errorf("redaction ate the surrounding prose: %q", decoded.Symbols[0].Doc)
	}
}

func TestText_PreservesUTF8AroundASecret(t *testing.T) {
	// Doc comments are the one indexed column most likely to be non-ASCII,
	// and byte-offset splicing is exactly where that goes wrong.
	in := "// año — clave: \n" + `password = "Xq7Lm2Vz9Pw4Rt6Yb1Nd8Kc3"` + "\n// año\n"
	got := Text(in)
	if !got.Redacted() {
		t.Fatalf("no finding in %q", in)
	}
	if strings.Count(got.Text, "año") != 2 || !strings.Contains(got.Text, "—") {
		t.Errorf("multibyte text was corrupted: %q", got.Text)
	}
}
