package websites

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
	"github.com/epicbyte/epicpanel/backend/internal/organizations"
	"github.com/google/uuid"
)

// SiteRoot is the filesystem boundary for the file manager. All operations
// are confined to /srv/epicpanel/websites/<websiteID>/ — enforced by
// resolving the real path and verifying containment (symlink-proof).
const SiteRoot = "/srv/epicpanel/websites"

// Register mounts the file manager routes (org-scoped + RBAC).
func (h *Handler) RegisterFiles(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/organizations/{org_id}/websites/{website_id}/files", h.requireOrg(organizations.RoleDeveloper, h.filesList))
	mux.HandleFunc("GET /v1/organizations/{org_id}/websites/{website_id}/files/content", h.requireOrg(organizations.RoleDeveloper, h.filesRead))
	mux.HandleFunc("PUT /v1/organizations/{org_id}/websites/{website_id}/files/content", h.requireOrg(organizations.RoleDeveloper, h.filesWrite))
	mux.HandleFunc("POST /v1/organizations/{org_id}/websites/{website_id}/files", h.requireOrg(organizations.RoleDeveloper, h.filesCreate))
	mux.HandleFunc("PATCH /v1/organizations/{org_id}/websites/{website_id}/files", h.requireOrg(organizations.RoleDeveloper, h.filesRename))
	mux.HandleFunc("DELETE /v1/organizations/{org_id}/websites/{website_id}/files", h.requireOrg(organizations.RoleDeveloper, h.filesDelete))
	mux.HandleFunc("GET /v1/organizations/{org_id}/websites/{website_id}/files/download", h.requireOrg(organizations.RoleDeveloper, h.filesDownload))
	mux.HandleFunc("POST /v1/organizations/{org_id}/websites/{website_id}/files/upload", h.requireOrg(organizations.RoleDeveloper, h.filesUpload))
}

// fileScope resolves the website (org-checked) and returns its site base dir.
func (h *Handler) fileScope(r *http.Request, orgID uuid.UUID) (base string, ws *Website, apiErr *httpapi.APIError) {
	ws, apiErr = h.websiteFromPath(r, orgID)
	if apiErr != nil {
		return "", nil, apiErr
	}
	return filepath.Join(SiteRoot, ws.ID.String()), ws, nil
}

// safeJoin resolves p within base and rejects escapes and symlink jumps.
func safeJoin(base, rel string) (string, error) {
	clean := path.Clean("/" + rel) // forces relative-to-root
	full := filepath.Join(base, clean)
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		if os.IsNotExist(err) {
			// For creation paths: resolve the deepest existing parent.
			parent := filepath.Dir(full)
			rp, err2 := filepath.EvalSymlinks(parent)
			if err2 != nil {
				return "", err
			}
			if rp != base && !strings.HasPrefix(rp, base+string(filepath.Separator)) {
				return "", fmt.Errorf("path escapes site boundary")
			}
			return full, nil
		}
		return "", err
	}
	baseResolved, err := filepath.EvalSymlinks(base)
	if err != nil {
		return "", err
	}
	if resolved != baseResolved && !strings.HasPrefix(resolved, baseResolved+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes site boundary")
	}
	return resolved, nil
}

type fileEntry struct {
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	Mode    string    `json:"mode"`
	ModTime time.Time `json:"modified"`
	IsDir   bool      `json:"is_dir"`
}

// GET .../files?path=sub/dir
func (h *Handler) filesList(w http.ResponseWriter, r *http.Request) {
	orgID, ok := OrgIDFromRequest(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	base, _, apiErr := h.fileScope(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	rel := r.URL.Query().Get("path")
	dir, err := safeJoin(base, rel)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrForbidden("invalid path"))
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			httpapi.RespondError(w, httpapi.ErrNotFound("directory not found"))
			return
		}
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	out := []fileEntry{}
	for _, e := range entries {
		// skip noisy system bits? show everything inside the boundary.
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, fileEntry{
			Name:    e.Name(),
			Size:    info.Size(),
			Mode:    info.Mode().Perm().String(),
			ModTime: info.ModTime(),
			IsDir:   e.IsDir(),
		})
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{
		"path":    "/" + strings.Trim(rel, "/"),
		"entries": out,
	})
}

// GET .../files/content?path=...
func (h *Handler) filesRead(w http.ResponseWriter, r *http.Request) {
	orgID, ok := OrgIDFromRequest(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	base, _, apiErr := h.fileScope(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	p, err := safeJoin(base, r.URL.Query().Get("path"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrForbidden("invalid path"))
		return
	}
	info, err := os.Stat(p)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrNotFound("file not found"))
		return
	}
	if info.IsDir() || info.Size() > 2*1024*1024 {
		httpapi.RespondError(w, httpapi.ErrValidation("only text files up to 2 MB can be edited in the browser"))
		return
	}
	content, err := os.ReadFile(p)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{
		"path":    r.URL.Query().Get("path"),
		"content": string(content),
		"size":    info.Size(),
	})
}

// PUT .../files/content  {"path": "...", "content": "..."}
func (h *Handler) filesWrite(w http.ResponseWriter, r *http.Request) {
	orgID, ok := OrgIDFromRequest(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	base, ws, apiErr := h.fileScope(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	var req struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	p, err := safeJoin(base, req.Path)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrForbidden("invalid path"))
		return
	}
	if len(req.Content) > 2*1024*1024 {
		httpapi.RespondError(w, httpapi.ErrValidation("file exceeds 2 MB editor limit"))
		return
	}
	if err := os.WriteFile(p, []byte(req.Content), 0o644); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	// Keep site ownership consistent.
	if uid, gid, err := siteOwnerIDsFromPath(base); err == nil {
		_ = os.Chown(p, uid, gid)
	}
	h.auditUser(r, &orgID, "file.written", "website", ws.ID.String(), map[string]any{"path": req.Path})
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// POST .../files  {"path": "newdir", "type": "dir"|"file"}
func (h *Handler) filesCreate(w http.ResponseWriter, r *http.Request) {
	orgID, ok := OrgIDFromRequest(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	base, ws, apiErr := h.fileScope(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	var req struct {
		Path string `json:"path"`
		Type string `json:"type"`
	}
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	p, err := safeJoin(base, req.Path)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrForbidden("invalid path"))
		return
	}
	if _, err := os.Lstat(p); err == nil {
		httpapi.RespondError(w, httpapi.ErrConflict("already exists"))
		return
	}
	if req.Type == "dir" {
		if err := os.MkdirAll(p, 0o755); err != nil {
			httpapi.RespondError(w, httpapi.ErrInternal(err))
			return
		}
	} else {
		if err := os.WriteFile(p, []byte{}, 0o644); err != nil {
			httpapi.RespondError(w, httpapi.ErrInternal(err))
			return
		}
	}
	h.auditUser(r, &orgID, "file.created", "website", ws.ID.String(), map[string]any{"path": req.Path, "type": req.Type})
	httpapi.WriteJSON(w, http.StatusCreated, map[string]any{"ok": true})
}

// PATCH .../files  {"path": "old", "to": "new"}  (rename/move)
func (h *Handler) filesRename(w http.ResponseWriter, r *http.Request) {
	orgID, ok := OrgIDFromRequest(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	base, ws, apiErr := h.fileScope(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	var req struct {
		Path string `json:"path"`
		To   string `json:"to"`
	}
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	from, err := safeJoin(base, req.Path)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrForbidden("invalid path"))
		return
	}
	if from == base { // never rename the site root
		httpapi.RespondError(w, httpapi.ErrValidation("cannot rename the site root"))
		return
	}
	to, err := safeJoin(base, req.To)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrForbidden("invalid destination"))
		return
	}
	if _, err := os.Lstat(to); err == nil {
		httpapi.RespondError(w, httpapi.ErrConflict("destination already exists"))
		return
	}
	if err := os.Rename(from, to); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.auditUser(r, &orgID, "file.renamed", "website", ws.ID.String(), map[string]any{"from": req.Path, "to": req.To})
	w.WriteHeader(http.StatusNoContent)
}

// DELETE .../files?path=...
func (h *Handler) filesDelete(w http.ResponseWriter, r *http.Request) {
	orgID, ok := OrgIDFromRequest(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	base, ws, apiErr := h.fileScope(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	rel := r.URL.Query().Get("path")
	p, err := safeJoin(base, rel)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrForbidden("invalid path"))
		return
	}
	if p == base {
		httpapi.RespondError(w, httpapi.ErrValidation("cannot delete the site root"))
		return
	}
	if err := os.RemoveAll(p); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.auditUser(r, &orgID, "file.deleted", "website", ws.ID.String(), map[string]any{"path": rel})
	w.WriteHeader(http.StatusNoContent)
}

// GET .../files/download?path=... — single file, or the whole site as a tar.gz when path is empty.
func (h *Handler) filesDownload(w http.ResponseWriter, r *http.Request) {
	orgID, ok := OrgIDFromRequest(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	base, ws, apiErr := h.fileScope(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	rel := r.URL.Query().Get("path")
	if rel == "" || rel == "/" {
		h.streamSiteArchive(w, r, base, ws)
		return
	}
	p, err := safeJoin(base, rel)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrForbidden("invalid path"))
		return
	}
	f, err := os.Open(p)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrNotFound("file not found"))
		return
	}
	defer f.Close()
	info, _ := f.Stat()
	if info.IsDir() {
		h.streamSiteArchive(w, r, p, ws)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(p)))
	_, _ = io.Copy(w, f)
}

func (h *Handler) streamSiteArchive(w http.ResponseWriter, r *http.Request, root string, ws *Website) {
	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()
		gz := gzip.NewWriter(pw)
		tw := tar.NewWriter(gz)
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			hdr, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			if rel == "." {
				return nil
			}
			hdr.Name = rel
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			if !info.IsDir() {
				f, err := os.Open(path)
				if err != nil {
					return err
				}
				if _, err := io.Copy(tw, f); err != nil {
					f.Close()
					return err
				}
				f.Close()
			}
			return nil
		})
		_ = tw.Close()
		_ = gz.Close()
		if err != nil {
			_ = pw.CloseWithError(err)
		}
	}()
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", ws.Name+"-files.tar.gz"))
	_, _ = io.Copy(w, pr)
}

// POST .../files/upload?path=... multipart form: file
func (h *Handler) filesUpload(w http.ResponseWriter, r *http.Request) {
	orgID, ok := OrgIDFromRequest(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	base, ws, apiErr := h.fileScope(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil { // 32 MB
		httpapi.RespondError(w, httpapi.ErrValidation("upload too large (32 MB limit)"))
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("missing file field"))
		return
	}
	defer file.Close()

	name := filepath.Base(hdr.Filename)
	if name == "" || name == "." || name == "/" || strings.Contains(name, "/") {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid filename"))
		return
	}
	dst, err := safeJoin(base, path.Join(r.URL.Query().Get("path"), name))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrForbidden("invalid path"))
		return
	}
	out, err := os.Create(dst)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	defer out.Close()
	if _, err := io.Copy(out, file); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if uid, gid, err := siteOwnerIDsFromPath(base); err == nil {
		_ = os.Chown(dst, uid, gid)
	}
	h.auditUser(r, &orgID, "file.uploaded", "website", ws.ID.String(), map[string]any{"path": r.URL.Query().Get("path"), "name": name})
	httpapi.WriteJSON(w, http.StatusCreated, map[string]any{"ok": true, "name": name})
}

// siteOwnerIDsFromPath returns the uid/gid owning the given directory.
func siteOwnerIDsFromPath(base string) (int, int, error) {
	info, err := os.Stat(base)
	if err != nil {
		return 0, 0, err
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return int(st.Uid), int(st.Gid), nil
	}
	return 0, 0, errors.New("stat unsupported")
}
