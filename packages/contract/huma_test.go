package contract

import (
	"context"
	"testing"
)

func TestHuma_ExtractsOperationFields(t *testing.T) {
	t.Parallel()

	idx := indexTestProject(t, "testdata/huma")
	e := NewExtractor(Options{ProjectRoot: "testdata/huma", SkipGraphQL: true, SkipTS: true})
	res, err := e.Extract(context.Background(), idx)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	humaDefs := filterByKind(res.Defs, KindHumaOp)
	if len(humaDefs) != 2 {
		t.Fatalf("expected 2 Huma defs, got %d: %+v", len(humaDefs), humaDefs)
	}

	var login, getSub *ContractDef
	for i := range humaDefs {
		switch humaDefs[i].Operation.OperationID {
		case "loginUser":
			login = &humaDefs[i]
		case "getPlatformSubscription":
			getSub = &humaDefs[i]
		}
	}
	if login == nil || getSub == nil {
		t.Fatalf("missing expected ops; loginUser=%v getPlatformSubscription=%v", login, getSub)
	}

	// login: annotation + http.MethodPost resolution
	if login.Operation.Method != "POST" {
		t.Errorf("login Method = %q, want POST (http.MethodPost should resolve)", login.Operation.Method)
	}
	if login.Operation.Path != "/api/v1/auth/login" {
		t.Errorf("login Path = %q", login.Operation.Path)
	}
	if login.FeatureID == nil || *login.FeatureID != "platform.auth.login" {
		t.Errorf("login FeatureID = %v, want platform.auth.login", login.FeatureID)
	}
	if login.Operation.Summary != "Log a user in" {
		t.Errorf("login Summary = %q", login.Operation.Summary)
	}
	if len(login.Operation.Tags) != 2 {
		t.Errorf("login Tags = %v, want 2 tags", login.Operation.Tags)
	}

	// getSub: NO annotation, string-literal Method ("GET")
	if getSub.Operation.Method != "GET" {
		t.Errorf("getSub Method = %q", getSub.Operation.Method)
	}
	if getSub.FeatureID != nil {
		t.Errorf("getSub FeatureID = %v, want nil (no annotation)", getSub.FeatureID)
	}
}

func TestHuma_HandlerSymbolResolution(t *testing.T) {
	t.Parallel()

	idx := indexTestProject(t, "testdata/huma")
	e := NewExtractor(Options{ProjectRoot: "testdata/huma", SkipGraphQL: true, SkipTS: true})
	res, _ := e.Extract(context.Background(), idx)

	for _, d := range res.Defs {
		if d.Kind == KindHumaOp && d.Operation.OperationID == "loginUser" {
			if d.Operation.HandlerSym == "" {
				t.Errorf("expected handler_sym resolved for loginUser, got empty (handler ref=%q)", d.Operation.Handler)
			}
			return
		}
	}
	t.Fatal("loginUser huma op not found in result")
}

func filterByKind(defs []ContractDef, kind ContractKind) []ContractDef {
	var out []ContractDef
	for _, d := range defs {
		if d.Kind == kind {
			out = append(out, d)
		}
	}
	return out
}
