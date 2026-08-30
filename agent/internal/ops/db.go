// Database management ops endpoints. The agent performs DDL through the
// dbops package (Go drivers, validated identifiers) — never a shell. Engines
// are only available when the operator configured admin credentials.
package ops

import (
	"context"
	"net/http"

	"github.com/epicbyte/epicpanel/agent/internal/dbops"
)

// engineOps resolves the configured Ops for an engine name.
func (s *Server) engineOps(engine string) (dbops.Ops, bool) {
	switch engine {
	case dbops.EngineMySQL:
		return s.mysql, s.mysql != nil
	case dbops.EnginePostgres:
		return s.postgres, s.postgres != nil
	default:
		return nil, false
	}
}

// handleDBEngines reports which engines are configured and reachable.
func (s *Server) handleDBEngines(w http.ResponseWriter, r *http.Request) {
	probe := func(ops dbops.Ops) map[string]any {
		if ops == nil {
			return map[string]any{"configured": false, "available": false}
		}
		out := map[string]any{"configured": true, "available": false}
		if err := ops.Ping(r.Context()); err != nil {
			out["error"] = "engine unreachable"
			return out
		}
		out["available"] = true
		if v, err := ops.Version(r.Context()); err == nil {
			out["version"] = v
		}
		return out
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"mysql":    probe(s.mysql),
		"postgres": probe(s.postgres),
	})
}

type dbCreateRequest struct {
	Engine string `json:"engine"`
	Name   string `json:"name"`
}

func (s *Server) handleDBCreate(w http.ResponseWriter, r *http.Request) {
	var req dbCreateRequest
	if !s.decode(w, r, &req) {
		return
	}
	ops, ok := s.engineOps(req.Engine)
	if !ok {
		s.writeError(w, r, &apiError{Status: 422, Code: "DB_ENGINE_UNAVAILABLE",
			Message: "database engine not configured on this server"})
		return
	}
	if err := ops.CreateDatabase(r.Context(), req.Name); err != nil {
		s.writeError(w, r, &apiError{Status: 422, Code: "DB_OPERATION_FAILED", Message: safe(err)})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"created": true})
}

func (s *Server) handleDBDrop(w http.ResponseWriter, r *http.Request) {
	var req dbCreateRequest
	if !s.decode(w, r, &req) {
		return
	}
	ops, ok := s.engineOps(req.Engine)
	if !ok {
		s.writeError(w, r, &apiError{Status: 422, Code: "DB_ENGINE_UNAVAILABLE",
			Message: "database engine not configured on this server"})
		return
	}
	if err := ops.DropDatabase(r.Context(), req.Name); err != nil {
		s.writeError(w, r, &apiError{Status: 422, Code: "DB_OPERATION_FAILED", Message: safe(err)})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"dropped": true})
}

type dbUserCreateRequest struct {
	Engine   string `json:"engine"`
	Database string `json:"database"`
	Username string `json:"username"`
}

// handleDBUserCreate creates a user and returns the generated password once.
func (s *Server) handleDBUserCreate(w http.ResponseWriter, r *http.Request) {
	var req dbUserCreateRequest
	if !s.decode(w, r, &req) {
		return
	}
	ops, ok := s.engineOps(req.Engine)
	if !ok {
		s.writeError(w, r, &apiError{Status: 422, Code: "DB_ENGINE_UNAVAILABLE",
			Message: "database engine not configured on this server"})
		return
	}
	password, err := dbops.GeneratePassword()
	if err != nil {
		s.writeError(w, r, serverError(err))
		return
	}
	if err := ops.CreateUser(r.Context(), req.Database, req.Username, password); err != nil {
		s.writeError(w, r, &apiError{Status: 422, Code: "DB_OPERATION_FAILED", Message: safe(err)})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"username": req.Username, "password": password})
}

type dbUserRequest struct {
	Engine   string `json:"engine"`
	Username string `json:"username"`
}

func (s *Server) handleDBUserDrop(w http.ResponseWriter, r *http.Request) {
	var req dbUserRequest
	if !s.decode(w, r, &req) {
		return
	}
	ops, ok := s.engineOps(req.Engine)
	if !ok {
		s.writeError(w, r, &apiError{Status: 422, Code: "DB_ENGINE_UNAVAILABLE",
			Message: "database engine not configured on this server"})
		return
	}
	if err := ops.DropUser(r.Context(), req.Username); err != nil {
		s.writeError(w, r, &apiError{Status: 422, Code: "DB_OPERATION_FAILED", Message: safe(err)})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"dropped": true})
}

// handleDBUserPassword rotates a user's password and returns the new one once.
func (s *Server) handleDBUserPassword(w http.ResponseWriter, r *http.Request) {
	var req dbUserRequest
	if !s.decode(w, r, &req) {
		return
	}
	ops, ok := s.engineOps(req.Engine)
	if !ok {
		s.writeError(w, r, &apiError{Status: 422, Code: "DB_ENGINE_UNAVAILABLE",
			Message: "database engine not configured on this server"})
		return
	}
	password, err := dbops.GeneratePassword()
	if err != nil {
		s.writeError(w, r, serverError(err))
		return
	}
	if err := ops.SetUserPassword(r.Context(), req.Username, password); err != nil {
		s.writeError(w, r, &apiError{Status: 422, Code: "DB_OPERATION_FAILED", Message: safe(err)})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"username": req.Username, "password": password})
}

var _ = context.Background // reserved for future async ops
