package api

import (
	"net/http"

	"github.com/epicbyte/epicpanel/backend/internal/auth"
	"github.com/epicbyte/epicpanel/backend/internal/databases"
	"github.com/epicbyte/epicpanel/backend/internal/httpx"
	"github.com/go-chi/chi/v5"
)

func (s *Server) dbActorFrom(r *http.Request) databases.Actor {
	a := databases.Actor{IP: httpx.ClientIP(r, s.cfg.Server.TrustedProxy)}
	if idt := auth.IdentityFrom(r.Context()); idt != nil {
		a.Label = idt.Username
		uid := idt.UserID
		a.ID = &uid
	}
	return a
}

type createDatabaseRequest struct {
	ServerID  string  `json:"server_id"`
	WebsiteID *string `json:"website_id"`
	Engine    string  `json:"engine"`
	Name      string  `json:"name"`
}

func (s *Server) handleDatabasesCreate(w http.ResponseWriter, r *http.Request) {
	var req createDatabaseRequest
	if err := Decode(r, &req); err != nil {
		Error(w, r, err)
		return
	}
	res, err := s.deps.Databases.Create(r.Context(), s.dbActorFrom(r), databases.CreateInput{
		ServerID:  req.ServerID,
		WebsiteID: req.WebsiteID,
		Engine:    req.Engine,
		Name:      req.Name,
	})
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusAccepted, map[string]any{"database": res.Database, "job": res.Job})
}

func (s *Server) handleDatabasesList(w http.ResponseWriter, r *http.Request) {
	out, err := s.deps.Databases.List(r.Context(),
		r.URL.Query().Get("server_id"), r.URL.Query().Get("website_id"))
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, map[string]any{"databases": out})
}

func (s *Server) handleDatabasesGet(w http.ResponseWriter, r *http.Request) {
	out, err := s.deps.Databases.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, out)
}

func (s *Server) handleDatabasesDelete(w http.ResponseWriter, r *http.Request) {
	job, err := s.deps.Databases.RequestDelete(r.Context(), s.dbActorFrom(r), chi.URLParam(r, "id"))
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusAccepted, map[string]any{"job": job})
}

type createDBUserRequest struct {
	Username string `json:"username"`
}

// handleDatabaseUsersCreate returns the generated password exactly once.
func (s *Server) handleDatabaseUsersCreate(w http.ResponseWriter, r *http.Request) {
	var req createDBUserRequest
	if err := Decode(r, &req); err != nil {
		Error(w, r, err)
		return
	}
	u, password, err := s.deps.Databases.CreateUser(r.Context(), s.dbActorFrom(r),
		chi.URLParam(r, "id"), req.Username)
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusCreated, map[string]any{"user": u, "password": password})
}

func (s *Server) handleDatabaseUsersDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.deps.Databases.DropUser(r.Context(), s.dbActorFrom(r),
		chi.URLParam(r, "id"), chi.URLParam(r, "userId")); err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, map[string]any{"deleted": true})
}

// handleDatabaseUserPassword rotates a user's password, returned once.
func (s *Server) handleDatabaseUserPassword(w http.ResponseWriter, r *http.Request) {
	password, err := s.deps.Databases.RotatePassword(r.Context(), s.dbActorFrom(r),
		chi.URLParam(r, "id"), chi.URLParam(r, "userId"))
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, map[string]any{"password": password})
}

// handleServerDBEngines proxies the agent's engine probe for the create form.
func (s *Server) handleServerDBEngines(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	target, err := s.deps.Servers.OpsTarget(r.Context(), id)
	if err != nil {
		Error(w, r, err)
		return
	}
	if !target.Manageable {
		JSON(w, r, http.StatusOK, map[string]any{
			"mysql":    map[string]any{"configured": false, "available": false},
			"postgres": map[string]any{"configured": false, "available": false},
		})
		return
	}
	engines, err := s.deps.Agent.DBEngines(r.Context(), target.AgentURL, target.OpsToken)
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, engines)
}
