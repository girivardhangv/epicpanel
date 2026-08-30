// Software management ops endpoints. The request only ever carries a
// component NAME (validated against the agent's allowlist registry) and a
// service action; the actual commands are constructed inside the software
// package from static provider definitions — never from request strings.
package ops

import (
	"net/http"
	"strings"
)

// handleSoftwareList returns detected components + the host OS info.
func (s *Server) handleSoftwareList(w http.ResponseWriter, r *http.Request) {
	comps := s.sw.List(r.Context())
	s.writeJSON(w, http.StatusOK, map[string]any{
		"os":         s.sw.OS(),
		"components": comps,
		"dir":        s.sw.Dir(),
	})
}

type softwareNameRequest struct {
	Name string `json:"name"`
}

func (s *Server) handleSoftwareInstall(w http.ResponseWriter, r *http.Request) {
	var req softwareNameRequest
	if !s.decode(w, r, &req) {
		return
	}
	res, err := s.sw.Install(r.Context(), req.Name)
	if err != nil {
		s.writeError(w, r, &apiError{Status: 422, Code: "SOFTWARE_INSTALL_FAILED", Message: safe(err)})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"installed": res.OK(), "output": tail(res.Stdout, res.Stderr)})
}

func (s *Server) handleSoftwareRemove(w http.ResponseWriter, r *http.Request) {
	var req softwareNameRequest
	if !s.decode(w, r, &req) {
		return
	}
	res, err := s.sw.Remove(r.Context(), req.Name)
	if err != nil {
		s.writeError(w, r, &apiError{Status: 422, Code: "SOFTWARE_REMOVE_FAILED", Message: safe(err)})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"removed": res.OK()})
}

type softwareServiceRequest struct {
	Name   string `json:"name"`
	Action string `json:"action"`
}

func (s *Server) handleSoftwareService(w http.ResponseWriter, r *http.Request) {
	var req softwareServiceRequest
	if !s.decode(w, r, &req) {
		return
	}
	res, err := s.sw.Service(r.Context(), req.Name, req.Action)
	if err != nil {
		s.writeError(w, r, &apiError{Status: 422, Code: "SOFTWARE_SERVICE_FAILED", Message: safe(err)})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": res.OK(), "output": tail(res.Stdout, res.Stderr)})
}

// tail returns a short combined output snippet for operator feedback.
func tail(stdout, stderr string) string {
	combined := strings.TrimSpace(stdout)
	if combined == "" {
		combined = strings.TrimSpace(stderr)
	}
	if len(combined) > 2000 {
		return combined[len(combined)-2000:]
	}
	return combined
}
