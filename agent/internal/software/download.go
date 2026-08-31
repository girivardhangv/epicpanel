package software

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// download fetches a URL to a temp file and returns its path. When wantSHA is
// non-empty the file is verified against it before being returned; any
// mismatch deletes the file and fails so a corrupt/hijacked archive never
// reaches extraction. Transient network/TLS failures (e.g. "bad record MAC"
// from flaky connections or TLS-inspecting intermediaries) are retried with a
// fresh connection — some upstream hosts, e.g. windows.php.net, drop streams
// under load.
func download(url, wantSHA string) (string, error) {
	const attempts = 4
	var lastErr error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			time.Sleep(time.Duration(i) * 2 * time.Second)
		}
		path, err := downloadOnce(url, wantSHA)
		if err == nil {
			return path, nil
		}
		lastErr = err
	}
	return "", lastErr
}

func downloadOnce(url, wantSHA string) (string, error) {
	// A fresh transport per attempt avoids reusing a half-dead connection and
	// pins IPv4, which sidesteps flaky IPv6/TLS paths on some hosts.
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          2,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   20 * time.Second,
		ExpectContinueTimeout: 5 * time.Second,
	}
	// Pin IPv4 for the DNS resolution to sidestep IPv6/TLS flakiness.
	transport.DialContext = func(ctx context.Context, _, addr string) (net.Conn, error) {
		d := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
		return d.DialContext(ctx, "tcp4", addr)
	}
	client := &http.Client{Timeout: 10 * time.Minute, Transport: transport}
	defer transport.CloseIdleConnections()
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}

	tmp, err := os.CreateTemp("", "epicpanel-archive-*")
	if err != nil {
		return "", err
	}
	defer tmp.Close()

	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), resp.Body)
	if err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	// Detect truncated downloads: a server that reports Content-Length but
	// delivers fewer bytes (flaky/limited connections) must be retried, not
	// silently extracted into a broken source tree (e.g. nginx with configure
	// present but auto/options missing).
	if resp.ContentLength > 0 && n != resp.ContentLength {
		os.Remove(tmp.Name())
		return "", fmt.Errorf("truncated download from %s (got %d of %d bytes)", url, n, resp.ContentLength)
	}
	if err := tmp.Sync(); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}

	if wantSHA != "" {
		got := hex.EncodeToString(h.Sum(nil))
		if !strings.EqualFold(got, wantSHA) {
			os.Remove(tmp.Name())
			return "", fmt.Errorf("checksum mismatch for %s (want %s, got %s)", url, wantSHA, got)
		}
	}
	return tmp.Name(), nil
}

// extractTo expands an archive into dst, stripping an optional top-level
// prefix (e.g. "nginx-1.27.4/") so the component lands directly in dst.
// Archive type is chosen from the Format field; anything else fails closed.
func extractTo(src, format, prefix, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	switch strings.ToLower(format) {
	case "zip":
		return extractZip(src, prefix, dst)
	case "tar.gz", "tgz":
		return extractTarGz(src, prefix, dst)
	case "tar.xz":
		return extractTarXz(src, prefix, dst)
	case "tar":
		return extractTar(src, prefix, dst)
	default:
		return fmt.Errorf("unsupported archive format %q", format)
	}
}

// safeJoin guards against zip-slip: the resolved path must stay under root.
func safeJoin(root, name string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(name))
	if clean == "." || filepath.IsAbs(clean) {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	// On Windows, a path starting with "\\" (volume-relative) is absolute
	// for the current drive but IsAbs reports false. Catch it here.
	if len(clean) > 0 && clean[0] == os.PathSeparator {
		return "", fmt.Errorf("unsafe archive path %q (volume-relative)", name)
	}
	joined := filepath.Join(root, clean)
	rel, err := filepath.Rel(root, joined)
	if err != nil {
		return "", fmt.Errorf("archive entry escapes destination: %q", name)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("archive entry escapes destination: %q", name)
	}
	return joined, nil
}

// stripPrefix removes the leading path component from an archive entry name
// when it matches the expected prefix directory. Returns "" for entries
// outside the prefix (skipped).
func stripPrefix(name, prefix string) string {
	if prefix == "" {
		return name
	}
	p := strings.TrimPrefix(filepath.ToSlash(name), "./")
	pref := strings.TrimSuffix(filepath.ToSlash(prefix), "/")
	if p == pref {
		return "" // the directory itself
	}
	if strings.HasPrefix(p, pref+"/") {
		return p[len(pref)+1:]
	}
	return "" // unrelated entry, skip
}
