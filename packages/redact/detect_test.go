package redact

import (
	"strings"
	"testing"
)

// The detector's contract is asymmetric, and these tables encode the
// asymmetry: a missed secret is a leak, but a false positive silently
// rewrites a legitimate query and destroys the analysis built on it. So the
// "must detect" table holds only shapes that cannot plausibly be anything
// else, and the "must not detect" table is the larger of the two.

// stripeShaped is a Stripe live-key SHAPE, assembled at runtime so the literal
// never appears intact anywhere in this file. It is not, and has never been, a
// real key.
//
// A detector for credentials needs inputs that look exactly like credentials,
// and GitHub's push protection blocks a push containing one -- correctly, since
// neither a scanner nor a reviewer skimming a diff can tell a fabricated
// Stripe-shaped key from a live one. Splitting the value across a concatenation
// leaves the bytes the detector sees identical and no matchable literal in the
// source.
//
// AKIAIOSFODNN7EXAMPLE below needs no such treatment: it is AWS's own published
// example key, which every scanner allowlists for exactly this reason. Prefer a
// vendor's documented example where one exists; assemble at runtime where none
// does. A new literal will pass review, pass CI, and then block the release
// push for whoever runs it next.
var stripeShaped = "sk_" + "live_" + "51H8xQ2eZvKYlo2C9dR7pW4nT"

func TestScan_DetectsHighSignalSecrets(t *testing.T) {
	cases := []struct {
		name   string
		text   string
		want   Kind
		secret string // the exact substring the finding must cover
	}{
		{
			name: "pkcs8 private key block",
			text: "const key = `-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBg\n-----END PRIVATE KEY-----`",
			want: KindPrivateKey,
			secret: "-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBg\n" +
				"-----END PRIVATE KEY-----",
		},
		{
			name: "rsa private key block",
			text: "-----BEGIN RSA PRIVATE KEY-----\nQUJDREVGRw==\n-----END RSA PRIVATE KEY-----",
			want: KindPrivateKey,
			secret: "-----BEGIN RSA PRIVATE KEY-----\nQUJDREVGRw==\n" +
				"-----END RSA PRIVATE KEY-----",
		},
		{
			name:   "unterminated private key still redacts to end of text",
			text:   "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXk",
			want:   KindPrivateKey,
			secret: "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXk",
		},
		{
			name:   "aws access key id",
			text:   "aws.Config{Creds: AKIAIOSFODNN7EXAMPLE}",
			want:   KindAWSAccessKey,
			secret: "AKIAIOSFODNN7EXAMPLE",
		},
		{
			name:   "aws temporary session key id",
			text:   "ASIAY34FZKBOKMUTVV7A was rotated",
			want:   KindAWSAccessKey,
			secret: "ASIAY34FZKBOKMUTVV7A",
		},
		{
			name:   "postgres dsn redacts only the password",
			text:   `dsn := "postgres://svc_orders:hunter2ZQ9xK1@db.internal:5432/orders"`,
			want:   KindConnectionString,
			secret: "hunter2ZQ9xK1",
		},
		{
			name:   "mongodb dsn with punctuation in password",
			text:   "mongodb+srv://root:p%40ss-W0rd!x@cluster0.mongodb.net/app",
			want:   KindConnectionString,
			secret: "p%40ss-W0rd!x",
		},
		{
			name:   "high entropy assignment to a secret-shaped name",
			text:   `apiKey = "` + stripeShaped + `"`,
			want:   KindAssignedSecret,
			secret: stripeShaped,
		},
		{
			name:   "go struct field assignment",
			text:   "cfg.ClientSecret: `Zx9Kq2mVbN7pLr4tYw8sHd3fGj6a`,",
			want:   KindAssignedSecret,
			secret: "Zx9Kq2mVbN7pLr4tYw8sHd3fGj6a",
		},
		{
			name:   "yaml style colon assignment",
			text:   "  access_token: '4f8c1b2e9d7a6350fe21bc94ad57e0f3'",
			want:   KindAssignedSecret,
			secret: "4f8c1b2e9d7a6350fe21bc94ad57e0f3",
		},
		{
			// snapshots.index_json holds doc comments as JSON strings, so a
			// credential quoted in a doc comment arrives with its quotes
			// backslash-escaped. Missing this would mean the single largest
			// disclosure atlas creates is also the one the sweep cannot read.
			name: "assignment inside a JSON-escaped doc comment",
			text: `{"symbols":[{"id":"cfg.Load",` +
				`"doc":"apiKey = \"` + stripeShaped + `\" is the fallback"}]}`,
			want:   KindAssignedSecret,
			secret: stripeShaped,
		},
		{
			name:   "json object field",
			text:   `{"client_secret": "Zx9Kq2mVbN7pLr4tYw8sHd3fGj6a", "issuer": "acme"}`,
			want:   KindAssignedSecret,
			secret: "Zx9Kq2mVbN7pLr4tYw8sHd3fGj6a",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Scan(tc.text)
			if len(got) != 1 {
				t.Fatalf("Scan(%q) returned %d findings, want exactly 1: %+v",
					tc.text, len(got), got)
			}
			f := got[0]
			if f.Kind != tc.want {
				t.Errorf("kind = %q, want %q", f.Kind, tc.want)
			}
			if f.Start < 0 || f.End > len(tc.text) || f.Start >= f.End {
				t.Fatalf("span [%d,%d) is not inside a %d-byte text", f.Start, f.End, len(tc.text))
			}
			if covered := tc.text[f.Start:f.End]; covered != tc.secret {
				t.Errorf("covered span = %q, want %q", covered, tc.secret)
			}
		})
	}
}

func TestScan_LeavesLegitimateTextAlone(t *testing.T) {
	// Every entry here appeared, in some form, in a real code index. A
	// detector that fires on any of them makes `atlas sql` lie about the
	// query it analysed.
	cases := []struct {
		name string
		text string
	}{
		{"parameterised predicate", "SELECT id FROM users WHERE password_hash = $1 AND token = $2"},
		{"named bind parameter", "UPDATE sessions SET token = :token WHERE id = :id"},
		{"question mark placeholder", "INSERT INTO creds (secret) VALUES (?)"},
		{"column list mentioning secrets", "SELECT api_key, client_secret FROM integrations"},
		{"env var indirection", `token = os.Getenv("GITHUB_TOKEN")`},
		{"shell style interpolation", `password = "${DB_PASSWORD}"`},
		{"template placeholder", `apiKey = "{{ .Values.apiKey }}"`},
		{"printf verb", `secret = fmt.Sprintf("%s-%s", a, b)`},
		{"obvious placeholder", `password = "changeme-please-now"`},
		{"masked value", `password = "****************"`},
		{"doc comment prose", "// The auth token is refreshed hourly by the sidecar process."},
		{"low entropy english phrase", `password = "supersecretvalue"`},
		{"short value", `token = "abc123"`},
		{"header constant", `authHeader = "Authorization"`},
		{"dsn without credentials", `dsn := "postgres://db.internal:5432/orders?sslmode=require"`},
		{"dsn with user but no password", `dsn := "postgres://svc_orders@db.internal:5432/orders"`},
		{"url with path segments", "https://github.com/sosalejandro/atlas/blob/main/README.md"},
		{"aws-like but wrong length", "AKIAIOSFODNN7EXAM"},
		{"go type named token", "type tokenBucket struct { rate int }"},
		{"json field with a prefixed name", `{"token_type": "Bearer", "expires_in": 3600}`},
		{"json field holding a path", `{"private_key_path": "/etc/ssl/keys/prod-service.pem"}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Scan(tc.text); len(got) != 0 {
				t.Errorf("Scan(%q) = %+v, want no findings", tc.text, got)
			}
		})
	}
}

func TestScan_ReportsLineAndContextWithoutLeakingTheSecret(t *testing.T) {
	text := "package cfg\n\nvar (\n\tdbPassword = \"Xq7Lm2Vz9Pw4Rt6Yb1Nd8Kc3\"\n)\n"
	got := Scan(text)
	if len(got) != 1 {
		t.Fatalf("Scan returned %d findings, want 1: %+v", len(got), got)
	}
	f := got[0]
	if f.Line != 4 {
		t.Errorf("Line = %d, want 4", f.Line)
	}
	if f.Context != "dbPassword" {
		t.Errorf("Context = %q, want %q", f.Context, "dbPassword")
	}
	if strings.Contains(f.Context, "Xq7Lm2") {
		t.Fatalf("Context leaks the secret it describes: %q", f.Context)
	}
	if f.Length != len("Xq7Lm2Vz9Pw4Rt6Yb1Nd8Kc3") {
		t.Errorf("Length = %d, want %d", f.Length, len("Xq7Lm2Vz9Pw4Rt6Yb1Nd8Kc3"))
	}
	if len(f.Digest) != digestLen {
		t.Errorf("Digest = %q, want %d hex chars", f.Digest, digestLen)
	}
	if strings.Contains(text, f.Digest) {
		t.Errorf("Digest %q appears verbatim in the source; it must be a hash", f.Digest)
	}
}

func TestScan_DigestIsStableAndDiscriminating(t *testing.T) {
	same := Scan(`token = "Xq7Lm2Vz9Pw4Rt6Yb1Nd8Kc3"`)
	again := Scan(`secret = "Xq7Lm2Vz9Pw4Rt6Yb1Nd8Kc3"`)
	other := Scan(`token = "Zz1Aa2Bb3Cc4Dd5Ee6Ff7Gg8"`)
	if len(same) != 1 || len(again) != 1 || len(other) != 1 {
		t.Fatalf("expected one finding each, got %d/%d/%d", len(same), len(again), len(other))
	}
	if same[0].Digest != again[0].Digest {
		t.Errorf("same secret in two places produced different digests %q / %q",
			same[0].Digest, again[0].Digest)
	}
	if same[0].Digest == other[0].Digest {
		t.Errorf("different secrets collided on digest %q", same[0].Digest)
	}
}

func TestScan_OverlappingMatchesCollapseToTheWidestSpan(t *testing.T) {
	// The assignment rule and the connection-string rule both fire here.
	// Emitting both would double-count the leak and produce a nested
	// replacement; the wider span wins because over-redacting a value
	// already known to be secret-shaped is the safe direction.
	text := `dbPassword = "postgres://svc:Kq9Xm2Vz7Pw4Rt6Y@db.internal:5432/app"`
	got := Scan(text)
	if len(got) != 1 {
		t.Fatalf("Scan returned %d findings, want 1: %+v", len(got), got)
	}
	if got[0].Kind != KindAssignedSecret {
		t.Errorf("kind = %q, want %q (the wider span)", got[0].Kind, KindAssignedSecret)
	}
}

func TestScan_FindingsAreOrderedByPosition(t *testing.T) {
	text := "AKIAIOSFODNN7EXAMPLE\n" +
		`apiKey = "` + stripeShaped + `"` + "\n" +
		"ASIAY34FZKBOKMUTVV7A\n"
	got := Scan(text)
	if len(got) != 3 {
		t.Fatalf("Scan returned %d findings, want 3: %+v", len(got), got)
	}
	for i := 1; i < len(got); i++ {
		if got[i].Start <= got[i-1].Start {
			t.Fatalf("findings are not sorted by start offset: %+v", got)
		}
	}
}

func TestShannonEntropy(t *testing.T) {
	if e := shannonEntropy(""); e != 0 {
		t.Errorf("entropy of empty string = %v, want 0", e)
	}
	if e := shannonEntropy("aaaaaaaa"); e != 0 {
		t.Errorf("entropy of a single repeated rune = %v, want 0", e)
	}
	// Four distinct symbols in equal proportion is exactly 2 bits per symbol.
	if e := shannonEntropy("abcdabcd"); e < 1.999 || e > 2.001 {
		t.Errorf("entropy of abcdabcd = %v, want 2", e)
	}
}
