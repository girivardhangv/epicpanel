package websites

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
)

// archiveEntryName validates one archive entry name BEFORE any filesystem
// touch: absolute paths, .., drive letters and backslash traversal are all
// rejected (zip-slip). The name must land inside the extraction root.
func archiveEntryName(name string) (string, error) {
	name = strings.ReplaceAll(name, "\\", "/")
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "..") {
		return "", fmt.Errorf("unsafe archive entry %q", name)
	}
	if len(name) >= 2 && name[1] == ':' { // windows drive-relative ("C:/...")
		return "", fmt.Errorf("unsafe archive entry %q", name)
	}
	clean := path.Clean(name)
	if clean == "." || clean == "" || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("unsafe archive entry %q", name)
	}
	return clean, nil
}

// extractLimits enforces the resource caps while streaming an archive.
type extractLimits struct {
	entries int
	total   int64
}

func (l *extractLimits) file(size int64) error {
	l.entries++
	if l.entries > extractMaxEntries {
		return fmt.Errorf("archive has too many entries (max %d)", extractMaxEntries)
	}
	if size > extractMaxEntrySize {
		return fmt.Errorf("archive entry too large (max %d MB per file)", extractMaxEntrySize>>20)
	}
	l.total += size
	if l.total > extractMaxTotalSize {
		return fmt.Errorf("archive expands beyond the %d GB extraction limit", extractMaxTotalSize>>30)
	}
	return nil
}

// POST .../files/extract {"path": "pkg.zip", "to": "target-dir", "overwrite": false}
// Supported formats: .zip, .tar, .tar.gz, .tgz. Entries are name-validated
// and size-capped BEFORE extraction (zip-slip + decompression-bomb guards);
// symlink/hardlink/device entries are refused (they are an escape vector
// inside a customer-writable tree).
func (h *Handler) filesExtract(w http.ResponseWriter, r *http.Request) {
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
		Path      string `json:"path"`
		To        string `json:"to"`
		Overwrite bool   `json:"overwrite"`
	}
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	archivePath, err := safeJoin(base, req.Path)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrForbidden("invalid archive path"))
		return
	}
	info, err := os.Stat(archivePath)
	if err != nil || info.IsDir() {
		httpapi.RespondError(w, httpapi.ErrNotFound("archive not found"))
		return
	}
	destDir := path.Dir(strings.Trim(req.Path, "/"))
	if req.To != "" {
		destDir = req.To
	}
	dest, err := safeJoin(base, destDir)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrForbidden("invalid destination"))
		return
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}

	f, err := os.Open(archivePath)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	defer f.Close()

	limits := &extractLimits{}
	var extracted int
	switch {
	case strings.HasSuffix(strings.ToLower(archivePath), ".zip"):
		extracted, err = extractZip(f, info.Size(), dest, req.Overwrite, limits)
	case strings.HasSuffix(strings.ToLower(archivePath), ".tar.gz"),
		strings.HasSuffix(strings.ToLower(archivePath), ".tgz"),
		strings.HasSuffix(strings.ToLower(archivePath), ".tar"):
		extracted, err = extractTar(f, strings.HasSuffix(strings.ToLower(archivePath), ".tar"), dest, req.Overwrite, limits)
	default:
		httpapi.RespondError(w, httpapi.ErrValidation("unsupported archive type (use .zip, .tar, .tar.gz or .tgz)"))
		return
	}
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("extraction failed: "+err.Error()))
		return
	}
	h.chownToSiteOwner(base, dest)
	h.auditUser(r, &orgID, "file.extracted", "website", ws.ID.String(),
		map[string]any{"path": req.Path, "to": destDir, "entries": extracted})
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "entries": extracted})
}

func extractZip(r io.ReaderAt, size int64, dest string, overwrite bool, limits *extractLimits) (int, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return 0, fmt.Errorf("not a valid zip archive: %w", err)
	}
	count := 0
	for _, zf := range zr.File {
		name, err := archiveEntryName(zf.Name)
		if err != nil {
			return count, err
		}
		if zf.Mode()&os.ModeSymlink != 0 || zf.Mode()&os.ModeDevice != 0 || zf.Mode()&os.ModeNamedPipe != 0 {
			return count, fmt.Errorf("refusing non-regular archive entry %q", zf.Name)
		}
		if zf.FileInfo().IsDir() {
			if err := limits.file(0); err != nil {
				return count, err
			}
			if err := os.MkdirAll(filepath.Join(dest, filepath.FromSlash(name)), 0o755); err != nil {
				return count, err
			}
			continue
		}
		if err := limits.file(int64(zf.UncompressedSize64)); err != nil {
			return count, err
		}
		target, err := extractTarget(dest, name, overwrite)
		if err != nil {
			return count, err
		}
		if target == "" {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			return count, err
		}
		// Stream-copy with the enforced cap: the header size is trusted only
		// as far as the limit check; the copy re-caps in case the header lies.
		if err := copyExtractFile(rc, target, extractMaxEntrySize); err != nil {
			rc.Close()
			return count, err
		}
		rc.Close()
		count++
	}
	return count, nil
}

func extractTar(r io.Reader, plain bool, dest string, overwrite bool, limits *extractLimits) (int, error) {
	src := r
	if !plain {
		gz, err := gzip.NewReader(r)
		if err != nil {
			return 0, fmt.Errorf("not a valid gzip archive: %w", err)
		}
		defer gz.Close()
		src = gz
	}
	tr := tar.NewReader(src)
	count := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return count, fmt.Errorf("corrupt archive: %w", err)
		}
		name, err := archiveEntryName(hdr.Name)
		if err != nil {
			return count, err
		}
		switch hdr.Typeflag {
		case tar.TypeReg, tar.TypeRegA, tar.TypeDir:
			// ok
		case tar.TypeXGlobalHeader, tar.TypeXHeader:
			continue // pax metadata, not a file
		default:
			return count, fmt.Errorf("refusing non-regular archive entry %q (type %c)", hdr.Name, hdr.Typeflag)
		}
		if err := limits.file(hdr.Size); err != nil {
			return count, err
		}
		if hdr.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(filepath.Join(dest, filepath.FromSlash(name)), 0o755); err != nil {
				return count, err
			}
			continue
		}
		target, err := extractTarget(dest, name, overwrite)
		if err != nil {
			return count, err
		}
		if target == "" {
			continue
		}
		if err := copyExtractFile(tr, target, hdr.Size); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

// extractTarget resolves one validated entry name to its destination path,
// creating parent dirs. Returns "" when the entry is skipped (existing file,
// overwrite=false). Re-verifies containment (defense in depth).
func extractTarget(dest, name string, overwrite bool) (string, error) {
	target := filepath.Join(dest, filepath.FromSlash(name))
	if target != dest && !strings.HasPrefix(target, dest+string(filepath.Separator)) {
		return "", fmt.Errorf("archive entry escapes the extraction directory")
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", err
	}
	if _, err := os.Lstat(target); err == nil {
		if !overwrite {
			return "", nil // skip existing
		}
		if err := os.RemoveAll(target); err != nil {
			return "", err
		}
	}
	return target, nil
}

// copyExtractFile streams src to path with a hard byte cap (a lying header
// must not defeat the decompression-bomb guard).
func copyExtractFile(src io.Reader, target string, cap int64) error {
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	n, err := io.Copy(out, io.LimitReader(src, cap+1))
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(target)
		return err
	}
	if n > cap {
		_ = os.Remove(target)
		return fmt.Errorf("archive entry exceeds the per-file limit")
	}
	return nil
}

// POST .../files/copy {"path": "from", "to": "to"} — recursive copy inside
// the site boundary (PATCH/rename already provides move; copy duplicates).
func (h *Handler) filesCopy(w http.ResponseWriter, r *http.Request) {
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
		httpapi.RespondError(w, httpapi.ErrForbidden("invalid source path"))
		return
	}
	if from == base {
		httpapi.RespondError(w, httpapi.ErrValidation("cannot copy the site root"))
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
	// The destination must not be INSIDE the source (infinite recursion).
	if strings.HasPrefix(to, from+string(filepath.Separator)) {
		httpapi.RespondError(w, httpapi.ErrValidation("destination is inside the source"))
		return
	}
	var copied int64
	var files int
	err = copyTree(from, to, &copied, &files)
	if err != nil {
		_ = os.RemoveAll(to) // no partial copies left behind
		httpapi.RespondError(w, httpapi.ErrValidation("copy failed: "+err.Error()))
		return
	}
	h.chownToSiteOwner(base, to)
	h.auditUser(r, &orgID, "file.copied", "website", ws.ID.String(),
		map[string]any{"from": req.Path, "to": req.To, "files": files})
	httpapi.WriteJSON(w, http.StatusCreated, map[string]any{"ok": true, "files": files})
}

// copyTree recursively copies files + directories (sizes capped).
func copyTree(from, to string, total *int64, files *int) error {
	info, err := os.Lstat(from)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil // never follow or duplicate symlinks across the tree
	}
	if info.IsDir() {
		if err := os.MkdirAll(to, info.Mode().Perm()); err != nil {
			return err
		}
		entries, err := os.ReadDir(from)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := copyTree(filepath.Join(from, e.Name()), filepath.Join(to, e.Name()), total, files); err != nil {
				return err
			}
		}
		return nil
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	*total += info.Size()
	if *total > copyMaxTotalSize {
		return fmt.Errorf("copy exceeds the %d GB limit", copyMaxTotalSize>>30)
	}
	src, err := os.Open(from)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(to, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return err
	}
	if err := dst.Close(); err != nil {
		return err
	}
	*files++
	return nil
}

// POST .../files/compress {"paths": ["a", "b/"], "to": "archive.zip"}
// Zips the selection (files or directories) into one .zip inside the site.
func (h *Handler) filesCompress(w http.ResponseWriter, r *http.Request) {
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
		Paths []string `json:"paths"`
		To    string   `json:"to"`
	}
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	if len(req.Paths) == 0 {
		httpapi.RespondError(w, httpapi.ErrValidation("select at least one file or folder to compress"))
		return
	}
	if !strings.HasSuffix(strings.ToLower(req.To), ".zip") {
		req.To += ".zip"
	}
	out, err := safeJoin(base, req.To)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrForbidden("invalid destination"))
		return
	}
	if _, err := os.Lstat(out); err == nil {
		httpapi.RespondError(w, httpapi.ErrConflict("destination already exists"))
		return
	}

	roots := make([]string, 0, len(req.Paths))
	for _, p := range req.Paths {
		resolved, err := safeJoin(base, p)
		if err != nil {
			httpapi.RespondError(w, httpapi.ErrForbidden("invalid path: "+p))
			return
		}
		if resolved == base {
			httpapi.RespondError(w, httpapi.ErrValidation("cannot compress the site root"))
			return
		}
		roots = append(roots, resolved)
	}

	zf, err := os.Create(out)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	zw := zip.NewWriter(zf)
	var files int
	var failed error
	for i, root := range roots {
		relRoot := strings.Trim(req.Paths[i], "/")
		err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return nil
			}
			rel, rerr := filepath.Rel(root, p)
			if rerr != nil {
				return rerr
			}
			name := relRoot
			if rel != "." {
				name = relRoot + "/" + filepath.ToSlash(rel)
			}
			if info.IsDir() {
				_, err := zw.Create(name + "/")
				return err
			}
			if !info.Mode().IsRegular() {
				return nil
			}
			hdr, cerr := zip.FileInfoHeader(info)
			if cerr != nil {
				return cerr
			}
			hdr.Name = name
			hdr.Method = zip.Deflate
			entry, cerr := zw.CreateHeader(hdr)
			if cerr != nil {
				return cerr
			}
			f, cerr := os.Open(p)
			if cerr != nil {
				return cerr
			}
			defer f.Close()
			files++
			_, cerr = io.Copy(entry, f)
			return cerr
		})
		if err != nil {
			failed = err
			break
		}
	}
	if cerr := zw.Close(); cerr != nil && failed == nil {
		failed = cerr
	}
	if cerr := zf.Close(); cerr != nil && failed == nil {
		failed = cerr
	}
	if failed != nil {
		_ = os.Remove(out)
		httpapi.RespondError(w, httpapi.ErrInternal(failed))
		return
	}
	h.chownToSiteOwner(base, out)
	h.auditUser(r, &orgID, "file.compressed", "website", ws.ID.String(),
		map[string]any{"paths": req.Paths, "to": req.To, "files": files})
	httpapi.WriteJSON(w, http.StatusCreated, map[string]any{"ok": true, "files": files})
}
