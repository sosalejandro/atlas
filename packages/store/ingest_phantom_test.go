package store

import (
	"reflect"
	"testing"

	"github.com/sosalejandro/atlas/packages/shared"
)

// TestExtractFeatureIDsFromAnnotation_DropsNonDottedPhantoms is the issue-#77
// regression at the materialization choke point: stray non-dotted tokens must
// never seed a feature, whether they arrive via ann.IDs or the ann.Raw
// fallback (the path that previously let `humatier`/`foundational` through).
func TestExtractFeatureIDsFromAnnotation_DropsNonDottedPhantoms(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		ann  shared.Annotation
		want []string
	}{
		{
			name: "raw fallback drops stray word + bare tag, keeps dotted",
			ann:  shared.Annotation{Kind: shared.AnnFeature, Raw: "billing.org-tiers humatier mocked"},
			want: []string{"billing.org-tiers"},
		},
		{
			name: "raw fallback single bare word -> empty",
			ann:  shared.Annotation{Kind: shared.AnnFeature, Raw: "foundational mocked"},
			want: []string{},
		},
		{
			name: "ids path drops non-dotted that slipped in",
			ann:  shared.Annotation{Kind: shared.AnnFeature, IDs: []string{"auth.login", "humatier"}},
			want: []string{"auth.login"},
		},
		{
			name: "multiple dotted kept, deduped",
			ann:  shared.Annotation{Kind: shared.AnnFeature, Raw: "auth.login auth.login billing.checkout real"},
			want: []string{"auth.login", "billing.checkout"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := extractFeatureIDsFromAnnotation(tc.ann)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
