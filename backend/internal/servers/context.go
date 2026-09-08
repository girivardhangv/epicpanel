package servers

import (
	"context"
	"net/http"

	"github.com/google/uuid"
)

type orgIDCtxKey struct{}
type agentSrvCtxKey struct{}

func withOrgID(ctx context.Context, id uuid.UUID) context.Context {
	return context.WithValue(ctx, orgIDCtxKey{}, id)
}

// OrgIDFromRequest returns the organization ID resolved by requireOrgRole.
func OrgIDFromRequest(r *http.Request) (uuid.UUID, bool) {
	id, ok := r.Context().Value(orgIDCtxKey{}).(uuid.UUID)
	return id, ok
}

func withAgentServer(ctx context.Context, srv *Server) context.Context {
	return context.WithValue(ctx, agentSrvCtxKey{}, srv)
}

// ServerFromAgentContext returns the server authenticated via agent token.
func ServerFromAgentContext(r *http.Request) (*Server, bool) {
	srv, ok := r.Context().Value(agentSrvCtxKey{}).(*Server)
	return srv, ok
}
