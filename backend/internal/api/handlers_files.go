package api

import (
	"net/http"

	"github.com/epicbyte/epicpanel/backend/internal/httpx"
	"github.com/epicbyte/epicpanel/backend/internal/auth"
	"github.com/epicbyte/epicpanel/backend/internal/files"
	"github.com/go-chi/chi/v5"
)

func (s *Server) filesActor(r *http.Request) files.Actor {
	id := auth.IdentityFrom(r.Context())
	return files.Actor{
		Label: id.Username,
		IP:    httpx.ClientIP(r, s.cfg.Server.TrustedProxy),
	}
}

func (s *Server) handleFilesList(w http.ResponseWriter, r *http.Request) {
	dir := r.URL.Query().Get("path")
	out, err := s.deps.Files.List(r.Context(), chi.URLParam(r, "id"), dir)
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, out)
}

func (s *Server) handleFilesRead(w http.ResponseWriter, r *http.Request) {
	file := r.URL.Query().Get("path")
	out, err := s.deps.Files.Read(r.Context(), chi.URLParam(r, "id"), file, 0)
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, out)
}

type filesWriteRequest struct {
	Path          string `json:"path"`
	ContentBase64 string `json:"content_base64"`
}

func (s *Server) handleFilesWrite(w http.ResponseWriter, r *http.Request) {
	var req filesWriteRequest
	if err := Decode(r, &req); err != nil {
		Error(w, r, err)
		return
	}
	content, err := files.DecodeContentBase64(req.ContentBase64)
	if err != nil {
		Error(w, r, err)
		return
	}
	if err := s.deps.Files.Write(r.Context(), s.filesActor(r), chi.URLParam(r, "id"), req.Path, content); err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, map[string]any{"written": true})
}

type filesMkdirRequest struct {
	Path string `json:"path"`
}

func (s *Server) handleFilesMkdir(w http.ResponseWriter, r *http.Request) {
	var req filesMkdirRequest
	if err := Decode(r, &req); err != nil {
		Error(w, r, err)
		return
	}
	if err := s.deps.Files.Mkdir(r.Context(), s.filesActor(r), chi.URLParam(r, "id"), req.Path); err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, map[string]any{"created": true})
}

type filesRemoveRequest struct {
	Path string `json:"path"`
}

func (s *Server) handleFilesRemove(w http.ResponseWriter, r *http.Request) {
	var req filesRemoveRequest
	if err := Decode(r, &req); err != nil {
		Error(w, r, err)
		return
	}
	if err := s.deps.Files.Remove(r.Context(), s.filesActor(r), chi.URLParam(r, "id"), req.Path); err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, map[string]any{"removed": true})
}

type filesRenameRequest struct {
	OldPath string `json:"old_path"`
	NewPath string `json:"new_path"`
}

func (s *Server) handleFilesRename(w http.ResponseWriter, r *http.Request) {
	var req filesRenameRequest
	if err := Decode(r, &req); err != nil {
		Error(w, r, err)
		return
	}
	if err := s.deps.Files.Rename(r.Context(), s.filesActor(r), chi.URLParam(r, "id"), req.OldPath, req.NewPath); err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, map[string]any{"renamed": true})
}