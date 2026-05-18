package shared

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
)

func TestSymbol_JSONRoundTrip(t *testing.T) {
	t.Parallel()

	sym := Symbol{
		ID:        SymbolID("AuthHandler.Login"),
		Kind:      KindHandler,
		Position:  FilePosition{Path: "src/auth/handler.go", Line: 42, Col: 5},
		Doc:       "Login authenticates a user.",
		Signature: "func (h *AuthHandler) Login(ctx context.Context, req LoginReq) (LoginResp, error)",
		Package:   "src/auth",
	}

	data, err := json.Marshal(sym)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got Symbol
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got != sym {
		t.Fatalf("round trip mismatch:\n want %+v\n  got %+v", sym, got)
	}
}

func TestFilePosition_OmitsZeroCol(t *testing.T) {
	t.Parallel()

	fp := FilePosition{Path: "x.go", Line: 1, Col: 0}
	data, err := json.Marshal(fp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"path":"x.go","line":1}`
	if string(data) != want {
		t.Fatalf("expected col to be omitted; got %s", data)
	}
}

func TestAnnotation_MultiID(t *testing.T) {
	t.Parallel()

	a := Annotation{
		Kind:     AnnFeature,
		IDs:      []string{"checkout.cart", "checkout.shipping"},
		Tags:     []string{"#real"},
		Source:   SourceAtlas,
		Position: FilePosition{Path: "e2e/checkout.spec.ts", Line: 7},
		Raw:      "checkout.cart checkout.shipping #real",
	}
	if len(a.IDs) != 2 {
		t.Fatalf("expected 2 ids, got %d", len(a.IDs))
	}
	if a.Source != SourceAtlas {
		t.Fatalf("expected SourceAtlas, got %s", a.Source)
	}
}

func TestSentinels_DistinctAndIsable(t *testing.T) {
	t.Parallel()

	if errors.Is(ErrFeatureNotFound, ErrSymbolNotFound) {
		t.Fatalf("ErrFeatureNotFound must not match ErrSymbolNotFound under errors.Is")
	}
	if !errors.Is(ErrFeatureNotFound, ErrFeatureNotFound) {
		t.Fatalf("errors.Is on identical sentinel must hold")
	}

	// Wrapping a sentinel must still match.
	wrapped := errors.Join(errors.New("ctx"), ErrSymbolNotFound)
	if !errors.Is(wrapped, ErrSymbolNotFound) {
		t.Fatalf("wrapped sentinel must satisfy errors.Is")
	}
}

func TestNopLogger_AcceptsAnyArgs(t *testing.T) {
	t.Parallel()

	var l Logger = NopLogger{}
	ctx := context.Background()
	// Should not panic regardless of arg shape.
	l.Debug(ctx, "d")
	l.Info(ctx, "i", "k", 1)
	l.Warn(ctx, "w", "k", "v", "k2", true)
	l.Error(ctx, "e", "k", nil)
}

func TestNewSlogLogger_WritesToWriter(t *testing.T) {
	t.Parallel()

	l := NewSlogLogger(io.Discard)
	if l == nil {
		t.Fatalf("expected non-nil logger")
	}
	l.Info(context.Background(), "smoke", "k", "v")
}
