package contract

import (
	"context"
	"testing"
)

func TestGraphQL_ExtractsMutationsAndQueries(t *testing.T) {
	t.Parallel()

	idx := indexTestProject(t, "testdata/graphql")
	e := NewExtractor(Options{ProjectRoot: "testdata/graphql", SkipTS: true})
	res, err := e.Extract(context.Background(), idx)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	gqlDefs := filterByKind(res.Defs, KindGraphQL)
	want := map[string]string{
		"Mutation.loginUser":  "Mutation",
		"Mutation.logoutUser": "Mutation",
		"Query.currentUser":   "Query",
	}
	got := map[string]string{}
	for _, d := range gqlDefs {
		got[d.Name] = d.Operation.GraphQLType
	}
	for name, kind := range want {
		if got[name] != kind {
			t.Errorf("GraphQL: %s graphql_type = %q, want %q (got=%v)", name, got[name], kind, got)
		}
	}

	// Line numbers must be tracked.
	for _, d := range gqlDefs {
		if d.Line == 0 {
			t.Errorf("GraphQL: line not tracked for %s", d.Name)
		}
	}
}

func TestGraphQL_EmptyProjectNoErrors(t *testing.T) {
	t.Parallel()

	idx := indexTestProject(t, "testdata/emptyproj")
	e := NewExtractor(Options{ProjectRoot: "testdata/emptyproj", SkipTS: true})
	res, err := e.Extract(context.Background(), idx)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	for _, d := range res.Defs {
		if d.Kind == KindGraphQL {
			t.Errorf("did not expect any GraphQL defs from emptyproj, got %+v", d)
		}
	}
	if len(res.Warnings) != 0 {
		t.Errorf("expected no warnings on emptyproj, got %v", res.Warnings)
	}
}
