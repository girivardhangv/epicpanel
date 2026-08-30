// Package install provides on-demand download-and-extract for the hosting
// runtime components (Nginx, PHP) that the panel can trigger through the
// agent ops channel. Only the explicit "install" action is supported — the
// agent never automatically installs anything during provisioning.
package install

import (
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const nginxVersion = "1.27.4"
const phpVersion = "8.3.9"

// Nginx downloads the official Windows build and places it at nginxDir.
// The resulting layout is <nginxDir>/nginx.exe, conf/, logs/, html/, etc.
func Nginx(nginxDir string) error {
	url := fmt.Sprintf("https://nginx.org/download/nginx-%s.zip", nginxVersion)
	zipPath, err := download(url)
	if err != nil {
		return fmt.Errorf("download nginx: %w", err)
	}
	defer os.Remove(zipPath)

	parent := filepath.Dir(nginxDir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	// nginx zip contains a single top-level folder "nginx-<version>/".
	if err := extractZipWithPrefix(zipPath, parent, "nginx-"+nginxVersion, nginxDir); err != nil {
		return fmt.Errorf("extract nginx: %w", err)
	}

	// Ensure the sites directory the agent expects.
	if err := os.MkdirAll(filepath.Join(nginxDir, "conf", "sites"), 0o755); err != nil {
		return err
	}
	// Ensure logs directory.
	if err := os.MkdirAll(filepath.Join(nginxDir, "logs"), 0o755); err != nil {
		return err
	}
	return nil
}

// PHP downloads the official Windows thread-safe build and extracts into
// phpDir (e.g. C:\PHP\8.4). Multiple versions can coexist.
func PHP(phpDir, version string) error {
	if version == "" {
		version = phpVersion
	}
	// Released builds live under /archives; current-dev snapshots use a
	// different naming and are intentionally not auto-installed.
	url := fmt.Sprintf("https://windows.php.net/downloads/releases/archives/php-%s-Win32-vs16-x64.zip", version)
	zipPath, err := download(url)
	if err != nil {
		return fmt.Errorf("download php: %w", err)
	}
	defer os.Remove(zipPath)

	parent := filepath.Dir(phpDir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	// The PHP zip has no top-level folder; files go directly into phpDir.
	if err := extractZip(zipPath, phpDir); err != nil {
		return fmt.Errorf("extract php: %w", err)
	}
	return nil
}

// download fetches a URL to a temp file and returns its path.
func download(url string) (string, error) {
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}
	tmp, err := os.CreateTemp("", "epicpanel-*.zip")
	if err != nil {
		return "", err
	}
	defer tmp.Close()
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return tmp.Name(), nil
}

// extractZip expands a zip archive into dst.
func extractZip(src, dst string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		fpath := filepath.Join(dst, f.Name)
		if !strings.HasPrefix(filepath.Clean(fpath), filepath.Clean(dst)+string(os.PathSeparator)) {
			continue // zip-slip guard
		}
		if f.FileInfo().IsDir() {
			os.MkdirAll(fpath, 0o755)
			continue
		}
		os.MkdirAll(filepath.Dir(fpath), 0o755)
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(fpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, rc)
		rc.Close()
		if err != nil {
			out.Close()
			return err
		}
		out.Close()
	}
	return nil
}

// extractZipWithPrefix extracts a single subdirectory from the zip and
// places its contents at dst. e.g. "nginx-1.27.4/" → <dst>/nginx.exe
func extractZipWithPrefix(src, dst, prefix, renameTo string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()
	prefixSlash := prefix + "/"
	for _, f := range r.File {
		if !strings.HasPrefix(f.Name, prefixSlash) {
			continue
		}
		rel := strings.TrimPrefix(f.Name, prefixSlash)
		dest := filepath.Join(dst, renameTo, rel)
		if !strings.HasPrefix(filepath.Clean(dest), filepath.Clean(dst)+string(os.PathSeparator)) {
			continue
		}
		if f.FileInfo().IsDir() {
			os.MkdirAll(dest, 0o755)
			continue
		}
		os.MkdirAll(filepath.Dir(dest), 0o755)
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, rc)
		rc.Close()
		if err != nil {
			out.Close()
			return err
		}
		out.Close()
	}
	return nil
}