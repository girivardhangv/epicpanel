package software

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// Regression test for nginx.org truncated downloads: a server advertising
// Content-Length larger than the bytes actually delivered must be rejected so
// the archive is never extracted into a broken source tree (configure present
// but auto/options missing).
func TestDownloadOnceRejectsTruncatedBody(t *testing.T) {
	full := []byte("nginx tarball contents\nwith several lines\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(full))) // advertise full size
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, string(full[:8])) // send only part, then close
	}))
	defer srv.Close()

	path, err := downloadOnce(srv.URL, "")
	if err == nil {
		os.Remove(path)
		t.Fatalf("expected truncated-download error, got none")
	}
	// Go's HTTP client reports the short body as unexpected EOF before our
	// Content-Length guard runs; either way the truncated archive must not be
	// accepted (otherwise extraction yields configure-but-no-auto/options).
	if !strings.Contains(err.Error(), "truncated download") &&
		!strings.Contains(err.Error(), "unexpected EOF") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDownloadOnceAcceptsFullBody(t *testing.T) {
	full := []byte("nginx tarball contents\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(full)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(full)
	}))
	defer srv.Close()

	path, err := downloadOnce(srv.URL, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer os.Remove(path)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if string(data) != string(full) {
		t.Fatalf("downloaded content mismatch")
	}
}
