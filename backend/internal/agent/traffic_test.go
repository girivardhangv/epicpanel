package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/epicbyte/epicpanel/backend/internal/agentproto"
)

// writeLog lays down a fake site tree with an access log containing lines.
func writeLog(t *testing.T, base, siteID string, lines []string, padTo int64) string {
	t.Helper()
	dir := filepath.Join(base, siteID, "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := ""
	for _, l := range lines {
		content += l + "\n"
	}
	if padTo > 0 { // simulate an existing log the sampler must seek past
		content = string(make([]byte, padTo)) + content
	}
	if err := os.WriteFile(filepath.Join(dir, "nginx-access.log"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "nginx-access.log")
}

const (
	lineBrowser = `203.0.113.10 - - [21/Sep/2026:10:00:00 +0000] "GET /index.html HTTP/1.1" 200 1043 "https://example.com/" "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 Chrome/119.0 Safari/537.36"`
	lineGoogle  = `66.249.66.1 - - [21/Sep/2026:10:00:01 +0000] "GET /robots.txt HTTP/1.1" 200 68 "-" "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)"`
	lineSQLMap  = `198.51.100.7 - - [21/Sep/2026:10:00:02 +0000] "GET /wp-admin.php?id=1 HTTP/1.1" 404 153 "-" "sqlmap/1.7#stable (http://sqlmap.org)"`
	lineEmpty   = `198.51.100.9 - - [21/Sep/2026:10:00:03 +0000] "POST /login HTTP/1.1" 200 31 "-" ""`
	lineMissing = `198.51.100.9 - - [21/Sep/2026:10:00:04 +0000] "GET /missing-page HTTP/1.1" 404 0 "-" "curl/8.1.2"`
)

const testWindowAdvance = 2 * time.Hour

// fakeClock is a cumulative test clock: each advance() jumps one window.
type fakeClock struct{ hours int64 }

func (c *fakeClock) now() time.Time {
	return time.Unix(1_789_000_000, 0).Add(time.Duration(c.hours) * time.Hour)
}
func (c *fakeClock) advance() { c.hours++ }

func newTestSampler(base string) (*TrafficSampler, *fakeClock) {
	s := NewTrafficSampler()
	s.logsBase = base
	clk := &fakeClock{}
	s.nowFn = clk.now
	return s, clk
}

func TestTrafficSamplerParsesCombinedLines(t *testing.T) {
	base := t.TempDir()
	logPath := writeLog(t, base, "11111111-1111-1111-1111-111111111111", []string{lineBrowser}, 0)
	s, clk := newTestSampler(base)

	// First pass: discovery, seek to EOF (nothing collected).
	if got := s.CollectCompleted(); len(got) != 0 {
		t.Fatalf("first pass emitted %d windows, want 0", len(got))
	}

	// Simulate live traffic appended after discovery. The browser IP leads
	// with 4 hits so TopIP is deterministic.
	extra := []string{lineBrowser, lineBrowser, lineBrowser, lineBrowser, lineGoogle, lineSQLMap, lineEmpty, lineMissing, lineMissing}
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range extra {
		f.WriteString(l + "\n")
	}
	f.Close()

	clk.advance()
	windows := s.CollectCompleted()
	if len(windows) != 1 {
		t.Fatalf("got %d windows, want 1", len(windows))
	}
	w := windows[0]
	if w.Requests != int64(len(extra)) {
		t.Fatalf("Requests = %d, want %d", w.Requests, len(extra))
	}
	if w.Status2xx != 6 || w.Status4xx != 3 || w.NotFoundReqs != 3 {
		t.Fatalf("status mix wrong: 2xx=%d 4xx=%d notfound=%d", w.Status2xx, w.Status4xx, w.NotFoundReqs)
	}
	if w.UAGoodBot != 1 || w.UABadTool != 3 || w.UAEmpty != 1 {
		t.Fatalf("UA classes wrong: good=%d bad=%d empty=%d", w.UAGoodBot, w.UABadTool, w.UAEmpty)
	}
	if w.PostReqs != 1 || w.GetReqs != 8 {
		t.Fatalf("method mix wrong: get=%d post=%d", w.GetReqs, w.PostReqs)
	}
	if w.UniqueIPs != 4 {
		t.Fatalf("UniqueIPs = %d, want 4", w.UniqueIPs)
	}
	if w.TopIP != "203.0.113.10" || w.TopIPShare <= 0 || w.Top3Share <= 0 {
		t.Fatalf("top-talker stats wrong: top=%q share=%.2f top3=%.2f", w.TopIP, w.TopIPShare, w.Top3Share)
	}
	if w.WindowS != 60 {
		t.Fatalf("WindowS = %d, want 60", w.WindowS)
	}
	if w.RefererReqs != 4 {
		t.Fatalf("RefererReqs = %d, want 4", w.RefererReqs)
	}
}

func TestTrafficSamplerIgnoresNonSiteDirs(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "not-a-uuid", "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeLog(t, base, "not-a-uuid", []string{lineBrowser}, 0)
	s, clk := newTestSampler(base)
	clk.advance()
	if got := s.CollectCompleted(); len(got) != 0 {
		t.Fatalf("non-uuid directory produced %d windows", len(got))
	}
}

func TestTrafficSamplerEmptyWindowAfterActivity(t *testing.T) {
	base := t.TempDir()
	id := "22222222-2222-2222-2222-222222222222"
	logPath := writeLog(t, base, id, []string{lineBrowser}, 0)
	s, clk := newTestSampler(base)
	s.CollectCompleted() // discovery
	f, _ := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(lineBrowser + "\n")
	f.Close()
	// window 1: traffic
	clk.advance()
	ws := s.CollectCompleted()
	if len(ws) != 1 || ws[0].Requests != 1 {
		t.Fatalf("want 1 active window, got %+v", ws)
	}
	// window 2: silent — must still be emitted (idle marker) exactly once.
	clk.advance()
	ws = s.CollectCompleted()
	if len(ws) != 1 || ws[0].Requests != 0 {
		t.Fatalf("want one idle marker window, got %+v", ws)
	}
	// window 3: still silent — no more markers.
	clk.advance()
	if ws := s.CollectCompleted(); len(ws) != 0 {
		t.Fatalf("want no windows for continued silence, got %d", len(ws))
	}
}

func TestTrafficSamplerRotation(t *testing.T) {
	base := t.TempDir()
	id := "33333333-3333-3333-3333-333333333333"
	logPath := writeLog(t, base, id, []string{lineBrowser, lineBrowser}, 0)
	s, clk := newTestSampler(base)
	s.CollectCompleted() // discovery (offset = EOF)

	// Rotate: truncate to a SMALLER fresh log (the sampler detects rotation
	// via size shrink) with different content.
	if err := os.WriteFile(logPath, []byte(lineGoogle+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	clk.advance()
	ws := s.CollectCompleted()
	if len(ws) != 1 || ws[0].Requests != 1 || ws[0].UAGoodBot != 1 {
		t.Fatalf("rotated log should yield exactly the new content, got %+v", ws)
	}
}

func TestTrafficSamplerPartialLineHeldBack(t *testing.T) {
	base := t.TempDir()
	id := "55555555-5555-5555-5555-555555555555"
	logPath := writeLog(t, base, id, []string{}, 0)
	s, clk := newTestSampler(base)
	s.CollectCompleted() // discovery

	// Append one full line and one partial (no trailing newline).
	f, _ := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(lineBrowser + "\n" + lineBrowser[:20])
	f.Close()
	clk.advance()
	if ws := s.CollectCompleted(); len(ws) != 1 || ws[0].Requests != 1 {
		t.Fatalf("partial line must not be counted: %+v", ws)
	}

	// Complete the partial line; it must now count exactly once.
	f, _ = os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(lineBrowser[20:] + "\n")
	f.Close()
	clk.advance()
	if ws := s.CollectCompleted(); len(ws) != 1 || ws[0].Requests != 1 {
		t.Fatalf("completed line must count exactly once: %+v", ws)
	}
}

func TestClassifyUserAgent(t *testing.T) {
	cases := map[string]uaClass{
		"":                                                    uaEmpty,
		"-":                                                   uaEmpty,
		"Mozilla/5.0 (compatible; Googlebot/2.1)":             uaGoodBot,
		"facebookexternalhit/1.1":                             uaGoodBot,
		"sqlmap/1.7":                                          uaBadTool,
		"curl/8.1.2":                                          uaBadTool,
		"python-requests/2.31":                                uaBadTool,
		"Go-http-client/2.0":                                  uaBadTool,
		"Mozilla/5.0 HeadlessChrome/120.0":                    uaHeadless,
		"Mozilla/5.0 (Windows NT 10.0) Firefox/121.0":         uaOther,
		"Mozilla/5.0 (Linux; Android 14; Pixel 8) Chrome/120": uaOther,
	}
	for ua, want := range cases {
		if got := classifyUserAgent(ua); got != want {
			t.Errorf("classifyUserAgent(%q) = %v, want %v", ua, got, want)
		}
	}
}

func TestSampleTrafficRoundTrip(t *testing.T) {
	// The Sample frame must carry traffic windows through JSON unchanged
	// (the stream transport marshals/unmarshals Sample data payloads).
	sample := agentproto.Sample{
		Traffic: []agentproto.SiteTraffic{{
			WebsiteID: "44444444-4444-4444-4444-444444444444", WindowS: 60, Requests: 42,
			UniqueIPs: 7, Top3Share: 0.9, UABadTool: 5, NotFoundReqs: 20,
		}},
	}
	b, err := json.Marshal(sample)
	if err != nil {
		t.Fatal(err)
	}
	var out agentproto.Sample
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Traffic) != 1 || out.Traffic[0].Requests != 42 || out.Traffic[0].UABadTool != 5 {
		t.Fatalf("traffic frame did not survive the round trip: %+v", out.Traffic)
	}
}
