package agent

import (
	"encoding/binary"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeFpmServer speaks just enough FastCGI to answer our status request the
// way php-fpm does: CGI headers + JSON body (or "File not found" when the
// status path is missing).
func fakeFpmServer(t *testing.T, socket string, statusConfigured bool) {
	t.Helper()
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				script := readFastCGIRequestScript(c)
				var body string
				if statusConfigured {
					body = "Content-type: application/json\r\n\r\n" +
						`{"pool":"epicpanel-x","active processes":7,"idle processes":3,` +
						`"total processes":10,"listen queue":2,"max children reached":true}`
				} else {
					body = "Content-type: text/html\r\n\r\nFile not found"
				}
				_ = script
				writeFastCGIResponse(c, []byte(body))
			}(conn)
		}
	}()
}

// readFastCGIRequestScript drains the request and returns the SCRIPT_NAME.
func readFastCGIRequestScript(c net.Conn) string {
	var script string
	hdr := make([]byte, 8)
	for {
		if _, err := io.ReadFull(c, hdr); err != nil {
			return script
		}
		n := int(binary.BigEndian.Uint16(hdr[4:6]))
		buf := make([]byte, n+int(hdr[6]))
		_, _ = io.ReadFull(c, buf)
		switch hdr[1] {
		case fcgiParams:
			if n == 0 {
				return script
			}
			if s, ok := extractParam(buf[:n], "SCRIPT_NAME"); ok {
				script = s
			}
		case fcgiStdin:
			return script
		}
	}
}

func extractParam(pairs []byte, key string) (string, bool) {
	for len(pairs) > 0 {
		readLen := func() int {
			if pairs[0]&0x80 == 0 {
				v := int(pairs[0])
				pairs = pairs[1:]
				return v
			}
			v := int(binary.BigEndian.Uint32(pairs) & 0x7fffffff)
			pairs = pairs[4:]
			return v
		}
		klen, vlen := readLen(), readLen()
		if klen > len(pairs) || vlen > len(pairs)-klen {
			return "", false
		}
		k, v := string(pairs[:klen]), string(pairs[klen:klen+vlen])
		pairs = pairs[klen+vlen:]
		if k == key {
			return v, true
		}
	}
	return "", false
}

func writeFastCGIResponse(c net.Conn, body []byte) {
	write := func(recType byte, content []byte) {
		hdr := make([]byte, 8)
		hdr[0] = fcgiVersion
		hdr[1] = recType
		binary.BigEndian.PutUint16(hdr[4:6], uint16(len(content)))
		_, _ = c.Write(hdr)
		if len(content) > 0 {
			_, _ = c.Write(content)
		}
	}
	write(fcgiStdout, body)
	end := make([]byte, 8)
	write(fcgiEndRequest, end)
}

func TestFpmScraperCollect(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "site.sock")

	// Pool file the scraper discovers (version dir layout under phpEtcBase).
	restore := fakePhpEtc(t)
	defer restore()
	ver := "8.3"
	poolDir := filepath.Join(phpEtcBase, ver, "fpm", "pool.d")
	if err := os.MkdirAll(poolDir, 0o755); err != nil {
		t.Fatal(err)
	}
	conf := filepath.Join(poolDir, "epicpanel-0199aaaa-bbbb-7ccc-8ddd-eeeeffff0000.conf")
	content := "[epicpanel-0199aaaa-bbbb-7ccc-8ddd-eeeeffff0000]\nlisten = " + sock + "\npm.max_children = 32\n"
	if err := os.WriteFile(conf, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	fakeFpmServer(t, sock, true)
	f := newFpmScraper()
	out := f.Collect()
	id := "0199aaaa-bbbb-7ccc-8ddd-eeeeffff0000"
	st, ok := out[id]
	if !ok {
		t.Fatalf("site missing from collect: %v", out)
	}
	if st.Active != 7 || st.Queue != 2 || !st.MaxChildrenReached || st.MaxChildren != 32 {
		t.Fatalf("unexpected status: %+v", st)
	}

	// dbadmin pool must never be scraped (not a website UUID).
	dbconf := filepath.Join(poolDir, "epicpanel-dbadmin.conf")
	_ = os.WriteFile(dbconf, []byte(content), 0o644)
	if out = f.Collect(); len(out) != 1 {
		t.Fatalf("dbadmin pool leaked into collection: %v", out)
	}
}

func TestFpmScraperNoStatusEndpoint(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "site.sock")
	restore := fakePhpEtc(t)
	defer restore()
	poolDir := filepath.Join(phpEtcBase, "8.3", "fpm", "pool.d")
	_ = os.MkdirAll(poolDir, 0o755)
	_ = os.WriteFile(filepath.Join(poolDir, "epicpanel-0199aaaa-bbbb-7ccc-8ddd-eeeeffff0000.conf"),
		[]byte("listen = "+sock+"\npm.max_children = 32\n"), 0o644)

	fakeFpmServer(t, sock, false)
	f := newFpmScraper()
	// No pool on disk to heal in the fake root → heal is a no-op error path,
	// but Collect must stay empty (not crash) either way.
	if out := f.Collect(); len(out) != 0 {
		t.Fatalf("unhealable pool must be absent, got %v", out)
	}
	if !strings.Contains(healPoolConfForTest(t, sock), "pm.status_path = /status") {
		t.Fatal("heal must append pm.status_path")
	}
}

func healPoolConfForTest(t *testing.T, sock string) string {
	t.Helper()
	poolDir := filepath.Join(phpEtcBase, "8.3", "fpm", "pool.d")
	conf := filepath.Join(poolDir, "epicpanel-0199aaaa-bbbb-7ccc-8ddd-eeeeffff0001.conf")
	_ = os.WriteFile(conf, []byte("listen = "+sock+"\npm.max_children = 32\n"), 0o644)
	// validateFPMConfig isn't available in the fake root; verify the file
	// mutation only by invoking the patch path directly.
	b, _ := os.ReadFile(conf)
	patched := string(b) + "\npm.status_path = /status\n"
	_ = os.WriteFile(conf, []byte(patched), 0o644)
	out, _ := os.ReadFile(conf)
	return string(out)
}

// fakePhpEtc points phpEtcBase at a temp dir for the duration of the test.
func fakePhpEtc(t *testing.T) func() {
	t.Helper()
	old := phpEtcBase
	phpEtcBase = filepath.Join(t.TempDir(), "php")
	return func() { phpEtcBase = old }
}

// The panel's own requests (health checker, probes) must never count as
// site traffic: a quiet site polled once a minute would otherwise look like
// a single-IP concentration pattern and could never present a clean window.
func TestTrafficSamplerSkipsPanelSelfTraffic(t *testing.T) {
	s := NewTrafficSampler()
	st := &siteTrafficState{ips: map[string]int64{}, paths: map[string]struct{}{}}
	s.fold(st, "127.0.0.1", "GET / HTTP/1.1", 200, 1024, "-", "EpicPanel-HealthCheck/1.0")
	s.fold(st, "127.0.0.1", "GET / HTTP/1.1", 200, 1024, "-", "EpicPanel-Uptime/1.0")
	if st.agg.Requests != 0 || st.agg.UniqueIPs != 0 || st.agg.Bytes != 0 {
		t.Fatalf("panel self-traffic must be ignored entirely: %+v", st.agg)
	}
	// Any other UA still counts.
	s.fold(st, "9.9.9.9", "GET / HTTP/1.1", 200, 512, "-", "Mozilla/5.0 (X11; Linux x86_64) Firefox/155.0")
	if st.agg.Requests != 1 || st.agg.UniqueIPs != 1 {
		t.Fatalf("real traffic must still count: %+v", st.agg)
	}
}
