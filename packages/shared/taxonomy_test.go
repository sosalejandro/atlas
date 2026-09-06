package shared

import "testing"

func TestClassifyNode(t *testing.T) {
	cases := []struct {
		name string
		qn   SymbolID
		path string
		want NodeClass
	}{
		{"go func", "auth.Login", "internal/auth/login.go", NodeClassDeclaration},
		{"go method", "AuthHandler.Login", "internal/auth/handler.go", NodeClassDeclaration},
		{"ts export", "src/pages/Login.tsx::Login", "src/pages/Login.tsx", NodeClassDeclaration},
		{"python module", "pkg.mod.helper", "pkg/mod.py", NodeClassDeclaration},

		// Anchor marked in the id.
		{"route", "route:/login", "", NodeClassAnchor},
		{"sqlc query", "sql:GetUserByEmail", "db/queries/users.sql", NodeClassAnchor},
		{"endpoint", "endpoint:POST /api/v1/auth/login", "", NodeClassAnchor},

		// Anchor marked in the position only -- pyscan's stubs carry a
		// perfectly ordinary dotted id and a reserved path.
		{"py external stub", "typing.List", "external:py", NodeClassAnchor},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyNode(tc.qn, tc.path); got != tc.want {
				t.Errorf("ClassifyNode(%q, %q) = %q, want %q", tc.qn, tc.path, got, tc.want)
			}
		})
	}
}

// A path that merely CONTAINS a reserved word is a declaration. The rule is
// a prefix on the whole string, not a substring search -- a repo with a
// directory called `routes/` must not have every file in it reclassified.
func TestClassifyNode_PrefixNotSubstring(t *testing.T) {
	for _, path := range []string{
		"internal/routes/login.go",
		"db/sql/users.go",
		"api/endpoint/handler.go",
		"vendor/external/lib.go",
	} {
		if got := ClassifyNode("pkg.Fn", path); got != NodeClassDeclaration {
			t.Errorf("ClassifyNode(_, %q) = %q, want declaration", path, got)
		}
	}
}

func TestNodeClass_Valid(t *testing.T) {
	if !NodeClassDeclaration.Valid() || !NodeClassAnchor.Valid() {
		t.Fatal("both members of the closed set must be valid")
	}
	// The zero value is the case that matters: a writer that never set the
	// class must not pass, because the store's refusal is what keeps a
	// forgotten column from silently meaning "declaration".
	if NodeClass("").Valid() {
		t.Error(`NodeClass("").Valid() = true, want false`)
	}
	if NodeClass("symbol").Valid() {
		t.Error(`NodeClass("symbol").Valid() = true, want false`)
	}
}
