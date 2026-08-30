package platform

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// FSOps is the constrained filesystem surface: every path must resolve inside
// the configured sites root, which is verified on the agent for each call.
// There is no generic read/write API — file contents only flow for panel-
// generated configuration and the default page.
type FSOps interface {
	MkdirAll(paths []string) error
	WriteFile(path string, content []byte) error
	Remove(path string) error
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
