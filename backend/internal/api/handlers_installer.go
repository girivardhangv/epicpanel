package api

import (
	"net/http"

	"github.com/epicbyte/epicpanel/backend/internal/installer"
)

type installerStatusResponse struct {
	Installed bool           `json:"installed"`
	Version   string         `json:"version"`
	Steps     map[string]bool `json:"steps"`
}

// status is intentionally readable even post-installation so the frontend
// knows which tree to render; every other endpoint refuses once locked.
func (s *Server) handleInstallerStatus(w http.ResponseWriter, r *http.Request) {
	row, err := s.deps.Installer.LoadStatus(r.Context())
	if err != nil {
		Error(w, r, err)
		return
	}
	steps := s.deps.Installer.StepVerification(r.Context())
	JSON(w, r, http.StatusOK, installerStatusResponse{
		Installed: row.Status == installer.StatusCompleted,
		Version:   s.deps.Version,
		Steps:     steps,
	})
}

func (s *Server) handleInstallerRequirements(w http.ResponseWriter, r *http.Request) {
	report, err := s.deps.Installer.CheckRequirements(r.Context())
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, report)
}

type databaseTestResponse struct {
	Reachable bool   `json:"reachable"`
	Message   string `json:"message,omitempty"`
}

func (s *Server) handleInstallerDatabaseTest(w http.ResponseWriter, r *http.Request) {
	out, err := s.deps.Installer.VerifyDatabase(r.Context())
	if err != nil {
		resp := databaseTestResponse{Reachable: false}
		if m, ok := out["message"].(string); ok {
			resp.Message = m
		} else {
			resp.Message = "Cannot reach PostgreSQL using the configured connection."
		}
		JSON(w, r, http.StatusServiceUnavailable, resp)
		return
	}
	JSON(w, r, http.StatusOK, databaseTestResponse{Reachable: true})
}

type dbConfigRequest struct {
	DSN string `json:"dsn"`
}

func (s *Server) handleInstallerDatabaseConfig(w http.ResponseWriter, r *http.Request) {
	var req dbConfigRequest
	if err := Decode(r, &req); err != nil {
		Error(w, r, err)
		return
	}
	out, err := s.deps.Installer.ReplaceDatabase(r.Context(), req.DSN)
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, out)
}

type licenseRequest struct {
	Key string `json:"license_key"`
}

func (s *Server) handleInstallerLicense(w http.ResponseWriter, r *http.Request) {
	var req licenseRequest
	if err := Decode(r, &req); err != nil {
		Error(w, r, err)
		return
	}
	info, err := s.deps.Installer.ActivateLicense(r.Context(), req.Key)
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, info)
}

type configurationRequest struct {
	SiteName string `json:"site_name"`
	Timezone string `json:"timezone"`
}

func (s *Server) handleInstallerConfiguration(w http.ResponseWriter, r *http.Request) {
	var req configurationRequest
	if err := Decode(r, &req); err != nil {
		Error(w, r, err)
		return
	}
	out, err := s.deps.Installer.ConfigurePanel(r.Context(), req.SiteName, req.Timezone, "")
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, out)
}

type administratorRequest struct {
	Username    string `json:"username"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Password    string `json:"password"`
	Confirm     string `json:"confirm_password"`
}

func (s *Server) handleInstallerAdministrator(w http.ResponseWriter, r *http.Request) {
	var req administratorRequest
	if err := Decode(r, &req); err != nil {
		Error(w, r, err)
		return
	}
	out, err := s.deps.Installer.CreateAdministrator(r.Context(),
		req.Username, req.Email, req.DisplayName, req.Password, req.Confirm)
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, out)
}

type securityRequest struct {
	MinPasswordLength      int `json:"min_password_length"`
	MaxFailedLogins        int `json:"max_failed_logins"`
	LockoutMinutes         int `json:"lockout_minutes"`
	SessionLifetimeMinutes int `json:"session_lifetime_minutes"`
}

func (s *Server) handleInstallerSecurity(w http.ResponseWriter, r *http.Request) {
	var req securityRequest
	if err := Decode(r, &req); err != nil {
		Error(w, r, err)
		return
	}
	out, err := s.deps.Installer.ApplySecurity(r.Context(),
		req.MinPasswordLength, req.MaxFailedLogins, req.LockoutMinutes, req.SessionLifetimeMinutes)
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, out)
}

type completeResponse struct {
	Completed  bool   `json:"completed"`
	InstanceID string `json:"instance_id"`
}

func (s *Server) handleInstallerComplete(w http.ResponseWriter, r *http.Request) {
	out, err := s.deps.Installer.Complete(r.Context())
	if err != nil {
		Error(w, r, err)
		return
	}
	completed, _ := out["completed"].(bool)
	instanceID, _ := out["instance_id"].(string)
	JSON(w, r, http.StatusOK, completeResponse{Completed: completed, InstanceID: instanceID})
}
