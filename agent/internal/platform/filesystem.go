package platform

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// FSOps is the constrained filesystem surface: every path must resolve inside
// the configured sites root, which is verified on the agent for each call.
// File contents only flow for panel-requested reads/writes (File Manager);
// there is no unrestricted read/write API.
type FSOps interface {
	MkdirAll(paths []string) error
	WriteFile(path string, content []byte) error
	Remove(path string) error
	List(path string) ([]FSEntry, error)
	ReadFile(path string, maxBytes int64) ([]byte, int64, bool, error)
	Rename(oldPath, newPath string) error
}

// FSEntry is one directory listing item (File Manager).
type FSEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	IsDir   bool   `json:"is_dir"`
	Size    int64  `json:"size"`
	Mode    string `json:"mode,omitempty"`
	ModTime string `json:"mod_time,omitempty"`
}

// fsOps implements FSOps over a fixed sites root.
type fsOps struct {
	sitesRoot string
}

func newFSOps(sitesRoot string) (FSOps, error) {
	root := strings.TrimRight(strings.TrimSpace(sitesRoot), `/\`)
	if root == "" {
		return nil, errors.New("sites root is not configured")
	}
	if err := validateSitesRoot(root); err != nil {
		return nil, err
	}
	return &fsOps{sitesRoot: root}, nil
}

// validateSitesRoot is build-tagged in filesystem_linux.go /
// filesystem_windows.go (absolute-path rules differ).
func (f *fsOps) resolve(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("path is required")
	}
	clean := filepath.Clean(path)
	root := filepath.Clean(f.sitesRoot)
	norm := func(p string) string { return strings.ReplaceAll(p, "\\", "/") }
	if norm(clean) != norm(root) && !strings.HasPrefix(norm(clean)+"/", norm(root)+"/") {
		return "", errors.New("path escapes the sites root")
	}
	// Symlink escape check for the final component's existing parents:
	// EvalSymlinks on the deepest existing ancestor must stay inside the root.
	if resolved, err := filepath.EvalSymlinks(filepath.Dir(clean)); err == nil {
		if norm(resolved) != norm(root) && !strings.HasPrefix(norm(resolved)+"/", norm(root)+"/") {
			return "", errors.New("path resolves outside the sites root")
		}
	}
	return clean, nil
}

func (f *fsOps) MkdirAll(paths []string) error {
	for _, p := range paths {
		resolved, err := f.resolve(p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(resolved, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func (f *fsOps) WriteFile(path string, content []byte) error {
	resolved, err := f.resolve(path)
	if err != nil {
		return err
	}
	tmp := resolved + ".tmp"
	if err := os.WriteFile(tmp, content, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, resolved)
}

// Remove deletes a file or directory tree. Refusing to remove the sites root
// itself is the last line of defense against a misbehaving panel.
func (f *fsOps) Remove(path string) error {
	resolved, err := f.resolve(path)
	if err != nil {
		return err
	}
	if filepath.Clean(resolved) == filepath.Clean(f.sitesRoot) {
		return errors.New("refusing to remove the sites root")
	}
	return os.RemoveAll(resolved)
}

// List returns the entries of a directory inside the sites root. Entries are
// sorted directories-first then by name for a stable File Manager view.
func (f *fsOps) List(path string) ([]FSEntry, error) {
	resolved, err := f.resolve(path)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(resolved)
	if err != nil {
		return nil, err
	}
	out := make([]FSEntry, 0, len(entries))
	for _, e := range entries {
		info, ierr := e.Info()
		size := int64(0)
		if ierr == nil {
			size = info.Size()
		}
		out = append(out, FSEntry{
			Name:    e.Name(),
			Path:    filepath.ToSlash(filepath.Join(resolved, e.Name())),
			IsDir:   e.IsDir(),
			Size:    size,
			Mode:    modeString(ierr, info),
			ModTime: modTimeString(ierr, info),
		})
	}
	// Sort directories first, then by name.
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

// ReadFile returns up to maxBytes of a file (bounded) with its total size and
// a truncated flag. Only files inside the sites root are readable.
func (f *fsOps) ReadFile(path string, maxBytes int64) ([]byte, int64, bool, error) {
	if maxBytes <= 0 || maxBytes > 8<<20 {
		maxBytes = 8 << 20
	}
	resolved, err := f.resolve(path)
	if err != nil {
		return nil, 0, false, err
	}
	fi, err := os.Stat(resolved)
	if err != nil {
		return nil, 0, false, err
	}
	if fi.IsDir() {
		return nil, 0, false, errors.New("path is a directory")
	}
	size := fi.Size()
	if size == 0 {
		return []byte{}, 0, false, nil
	}
	readN := size
	truncated := false
	if readN > maxBytes {
		readN = maxBytes
		truncated = true
	}
	fh, err := os.Open(resolved)
	if err != nil {
		return nil, 0, false, err
	}
	defer fh.Close()
	buf := make([]byte, readN)
	n, err := io.ReadFull(fh, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, 0, false, err
	}
	return buf[:n], size, truncated, nil
}

// Rename moves or renames a file/directory inside the sites root. Both sides
// are validated independently so a move can never escape the root.
func (f *fsOps) Rename(oldPath, newPath string) error {
	if strings.TrimSpace(oldPath) == "" || strings.TrimSpace(newPath) == "" {
		return errors.New("old and new paths are required")
	}
	oldResolved, err := f.resolve(oldPath)
	if err != nil {
		return err
	}
	newResolved, err := f.resolve(newPath)
	if err != nil {
		return err
	}
	if filepath.Clean(oldResolved) == filepath.Clean(f.sitesRoot) ||
		filepath.Clean(newResolved) == filepath.Clean(f.sitesRoot) {
		return errors.New("refusing to rename the sites root")
	}
	if err := os.MkdirAll(filepath.Dir(newResolved), 0o755); err != nil {
		return err
	}
	return os.Rename(oldResolved, newResolved)
}

func modeString(err error, info os.FileInfo) string {
	if err != nil || info == nil {
		return ""
	}
	return info.Mode().String()
}

func modTimeString(err error, info os.FileInfo) string {
	if err != nil || info == nil {
		return ""
	}
	return info.ModTime().UTC().Format(time.RFC3339)
}

// ChownSiteTree changes ownership of a tree inside the sites root to a user.
// The path is validated against the sites root before any chown happens.
// Implementation is platform-specific (filesystem_linux.go /
// filesystem_windows.go); the Linux build delegates to the syscall walker.
func ChownSiteTree(sitesRoot, path, user string) error {
	root := strings.TrimRight(strings.TrimSpace(sitesRoot), `/\`)
	if root == "" {
		return errors.New("sites root is not configured")
	}
	clean := filepath.Clean(path)
	norm := func(p string) string { return strings.ReplaceAll(p, "\\", "/") }
	normRoot := norm(root)
	if norm(clean) != normRoot && !strings.HasPrefix(norm(clean)+"/", normRoot+"/") {
		return errors.New("path escapes the sites root")
	}
	return chownTree(clean, user)
}

// ReadLogBounded returns the tail of a log file without ever holding more
// than maxBytes in memory. Shared across platforms; callers have already
// validated the path.
func ReadLogBounded(path string, maxBytes int64) (content string, size int64, truncated bool, err error) {
	if maxBytes <= 0 || maxBytes > 512*1024 {
		maxBytes = 512 * 1024
	}
	fh, err := os.Open(path)
	if err != nil {
		return "", 0, false, err
	}
	defer fh.Close()

	st, err := fh.Stat()
	if err != nil {
		return "", 0, false, err
	}
	size = st.Size()
	if size == 0 {
		return "", 0, false, nil
	}
	offset := size - maxBytes
	if offset > 0 {
		truncated = true
	} else {
		offset = 0
	}
	if _, err := fh.Seek(offset, 0); err != nil {
		return "", size, truncated, err
	}
	buf := make([]byte, size-offset)
	n, err := fh.Read(buf)
	if err != nil && n == 0 {
		return "", size, truncated, err
	}
	// Start after the first newline so a tail never begins mid-line.
	text := string(buf[:n])
	if truncated {
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			text = text[i+1:]
		}
	}
	return text, size, truncated, nil
}

// LogsRead handles path validation + bounded read for the ops endpoint.
// Allowed: inside the sites root, or the platform nginx log directory.
func LogsRead(sitesRoot, path string, maxBytes int64) (string, int64, bool, error) {
	clean, err := validatedLogPath(sitesRoot, path)
	if err != nil {
		return "", 0, false, err
	}
	return ReadLogBounded(clean, maxBytes)
}

// allowedLogDir reports whether a directory may be read for logs.
func allowedLogDir(dir string) bool {
	norm := strings.ReplaceAll(strings.TrimRight(dir, `/\`), "\\", "/")
	for _, allowed := range logDirs() {
		a := strings.ReplaceAll(strings.TrimRight(allowed, `/\`), "\\", "/")
		if strings.EqualFold(norm, a) || strings.HasPrefix(norm+"/", a+"/") {
			return true
		}
	}
	return false
}

func validatedLogPath(sitesRoot, path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("path is required")
	}
	clean := filepath.Clean(path)
	norm := strings.ReplaceAll(clean, "\\", "/")

	root := strings.ReplaceAll(strings.TrimRight(sitesRoot, `/\`), "\\", "/")
	if strings.HasPrefix(norm+"/", root+"/") {
		return clean, nil
	}
	if allowedLogDir(filepath.Dir(clean)) {
		return clean, nil
	}
	return "", errors.New("log path is outside the allowed directories")
}
