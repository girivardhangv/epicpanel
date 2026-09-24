package websites

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

// Routes reach website handlers through two wrappers: requireOrg sets
// orgIDCtxKey, while the api layer's inline routes (one-click WordPress,
// Laravel, site commands) inject OrgKeyType. OrgIDFromRequest must accept
// both — a route/wrapper key mismatch turns into a 500 "org id missing
// from request context" (regression: laravel install).
func TestOrgIDFromRequestAcceptsBothWrapperKeys(t *testing.T) {
	orgID := uuid.New()
	cases := []struct {
		name string
		ctx  context.Context
	}{
		{"requireOrg key", context.WithValue(context.Background(), orgIDCtxKey{}, orgID)},
		{"api wrapper key", context.WithValue(context.Background(), OrgKeyType{}, orgID)},
	}
	for _, tc := range cases {
		r := httptest.NewRequest(http.MethodPost, "/v1/organizations/"+orgID.String()+"/websites/x/laravel", nil).WithContext(tc.ctx)
		got, ok := OrgIDFromRequest(r)
		if !ok || got != orgID {
			t.Errorf("%s: OrgIDFromRequest = (%v, %v), want (%v, true)", tc.name, got, ok, orgID)
		}
	}
}

func TestOrgIDFromRequestMissing(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/websites", nil)
	if _, ok := OrgIDFromRequest(r); ok {
		t.Error("OrgIDFromRequest reported ok without an org in context")
	}
}
