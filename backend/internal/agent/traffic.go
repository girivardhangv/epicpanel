package agent

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/epicbyte/epicpanel/backend/internal/agentproto"
	"github.com/google/uuid"
)

// ============================================================================
// Traffic sampling (dynamic resources feature).
//
// The agent owns the data plane, so it owns traffic observation: nginx writes
// per-site access logs at <sitesBase>/<website_id>/logs/nginx-access.log
// (default combined format). The sampler delta-reads each log, aggregates one
// window per site and attaches COMPLETED windows to the next metrics frame —
// raw features only, never verdicts. Bot scoring lives in the control plane
// (internal/traffic) so thresholds are tunable without agent updates and
// every site decision stays auditable panel-side.
// ============================================================================

const (
	defaultTrafficWindow = 60 * time.Second
	// Per-window caps keep memory bounded under floods. When the IP cap is
	// hit the excess requests still count (TruncatedIPs) — saturation is
	// itself an attack signal.
	defaultMaxIPs   = 1024
	defaultMaxPaths = 256
)

// TrafficSampler aggregates per-site access-log deltas into windows.
type TrafficSampler struct {
	mu       sync.Mutex
	logsBase string
	window   time.Duration
	maxIPs   int
	maxPaths int
	nowFn    func() time.Time

	state map[string]*siteTrafficState
}

type siteTrafficState struct {
	offset  int64
	partial []byte
	opened  time.Time
	agg     agentproto.SiteTraffic
	ips     map[string]int64
	paths   map[string]struct{}
	// everActive marks a site that produced at least one non-empty window,
	// so the first idle window after activity is emitted too — the control
	// plane sees traffic stopped instead of inferring it from staleness.
	everActive bool
}

// NewTrafficSampler wires the sampler with production defaults.
func NewTrafficSampler() *TrafficSampler {
	window := defaultTrafficWindow
	if v := os.Getenv("EPICPANEL_AGENT_TRAFFIC_WINDOW"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			window = d
		}
	}
	return &TrafficSampler{
		logsBase: "/srv/epicpanel/websites",
		window:   window,
		maxIPs:   defaultMaxIPs,
		maxPaths: defaultMaxPaths,
		nowFn:    time.Now,
		state:    map[string]*siteTrafficState{},
	}
}

// CollectCompleted closes due windows and returns them. Called from the
// sample loop; cheap (delta reads only).
func (t *TrafficSampler) CollectCompleted() []agentproto.SiteTraffic {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.nowFn()
	completed := []agentproto.SiteTraffic{}
	entries, err := os.ReadDir(t.logsBase)
	if err != nil {
		return completed
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		id := e.Name()
		if _, err := uuid.Parse(id); err != nil {
			continue // not a website directory
		}
		logPath := filepath.Join(t.logsBase, id, "logs", "nginx-access.log")
		st := t.state[id]
		if st == nil {
			// First sight: start observing from "now" (seek to EOF) so an
			// agent restart never replays hours of stale log as an attack.
			var size int64
			if fi, err := os.Stat(logPath); err == nil {
				size = fi.Size()
			}
			t.state[id] = &siteTrafficState{
				offset: size,
				opened: now,
				ips:    map[string]int64{},
				paths:  map[string]struct{}{},
			}
			continue
		}
		t.readDelta(logPath, st)
		if now.Sub(st.opened) < t.window {
			continue
		}
		finalizeWindow(st)
		if st.agg.Requests > 0 || st.everActive {
			agg := st.agg
			agg.WindowS = int(t.window.Seconds())
			agg.WebsiteID = id
			completed = append(completed, agg)
			st.everActive = st.agg.Requests > 0
		}
		st.opened = now
		st.agg = agentproto.SiteTraffic{}
		st.ips = map[string]int64{}
		st.paths = map[string]struct{}{}
	}
	// Forget state for removed sites (leak guard; runs rarely).
	if len(t.state) > len(entries)*2+16 {
		for id := range t.state {
			if _, err := os.Stat(filepath.Join(t.logsBase, id)); os.IsNotExist(err) {
				delete(t.state, id)
			}
		}
	}
	return completed
}

// finalizeWindow derives the share features that need the full per-IP map
// (top talker + top-3 concentration) before the map is reset with the window.
func finalizeWindow(st *siteTrafficState) {
	if st.agg.Requests <= 0 {
		return
	}
	type ipCount struct {
		ip string
		n  int64
	}
	counts := make([]ipCount, 0, len(st.ips))
	for ip, n := range st.ips {
		counts = append(counts, ipCount{ip, n})
	}
	sort.Slice(counts, func(i, j int) bool { return counts[i].n > counts[j].n })
	if len(counts) > 0 {
		st.agg.TopIP = counts[0].ip
		st.agg.TopIPShare = float64(counts[0].n) / float64(st.agg.Requests)
	}
	top3 := int64(0)
	for i := 0; i < len(counts) && i < 3; i++ {
		top3 += counts[i].n
	}
	st.agg.Top3Share = float64(top3) / float64(st.agg.Requests)
}

// readDelta consumes new bytes from one access log into the window aggregate.
func (t *TrafficSampler) readDelta(logPath string, st *siteTrafficState) {
	f, err := os.Open(logPath) // #nosec G304 — layout is the panel's own UUID directory scheme
	if err != nil {
		return // no log yet (site not serving) — fine
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return
	}
	if st.offset > fi.Size() {
		// Truncated/rotated: start over, keep the open window.
		st.offset = 0
		st.partial = nil
	}
	if fi.Size() == st.offset {
		return
	}
	if _, err := f.Seek(st.offset, 0); err != nil {
		return
	}
	consumed := int64(0)
	r := bufio.NewReaderSize(f, 64*1024)
	for {
		chunk, err := r.ReadBytes('\n')
		consumed += int64(len(chunk))
		if len(chunk) > 0 {
			var line []byte
			if len(st.partial) > 0 {
				line = append(st.partial, chunk...)
				st.partial = nil
			} else {
				line = chunk
			}
			if err == nil {
				t.parseLine(st, string(line))
			} else {
				// EOF without trailing newline: hold the partial line.
				st.partial = line
			}
		}
		if err != nil {
			break
		}
	}
	// Only bytes consumed as complete lines advance the offset; the partial
	// tail stays pending and is re-read (from the same offset) next pass.
	st.offset += consumed - int64(len(st.partial))
}

// parseLine folds one combined-format access-log line into the aggregate.
// Format: ip - user [time] "METHOD path PROTO" status bytes "referer" "ua"
func (t *TrafficSampler) parseLine(st *siteTrafficState, line string) {
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return
	}
	// remote_addr
	sp1 := strings.IndexByte(line, ' ')
	if sp1 <= 0 {
		return
	}
	ip := line[:sp1]
	rest := line[sp1+1:]
	// skip ident (-) and authenticated user (-)
	for i := 0; i < 2; i++ {
		sp := strings.IndexByte(rest, ' ')
		if sp < 0 {
			return
		}
		rest = rest[sp+1:]
	}
	// [time]
	lb := strings.IndexByte(rest, '[')
	rb := strings.IndexByte(rest, ']')
	if lb < 0 || rb < lb {
		return
	}
	rest = strings.TrimPrefix(rest[rb+1:], " ")
	// "request"
	q1 := strings.IndexByte(rest, '"')
	if q1 < 0 {
		return
	}
	rest = rest[q1+1:]
	q2 := strings.IndexByte(rest, '"')
	if q2 < 0 {
		return
	}
	request := rest[:q2]
	rest = rest[q2+1:]
	rest = strings.TrimPrefix(rest, " ")
	// status
	spS := strings.IndexByte(rest, ' ')
	if spS < 0 {
		return
	}
	status, _ := strconv.Atoi(rest[:spS])
	rest = rest[spS+1:]
	// bytes
	spB := strings.IndexByte(rest, ' ')
	var bytesSent int64
	if spB < 0 {
		bytesSent, _ = strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
		rest = ""
	} else {
		bytesSent, _ = strconv.ParseInt(rest[:spB], 10, 64)
		rest = rest[spB+1:]
	}
	// "referer" "ua"
	var referer, ua string
	if q1 = strings.IndexByte(rest, '"'); q1 >= 0 {
		rest = rest[q1+1:]
		if q2 = strings.IndexByte(rest, '"'); q2 >= 0 {
			referer = rest[:q2]
			tail := rest[q2+1:]
			if q1 = strings.IndexByte(tail, '"'); q1 >= 0 {
				tail = tail[q1+1:]
				if q2 = strings.LastIndexByte(tail, '"'); q2 >= 0 {
					ua = tail[:q2]
				}
			}
		}
	}
	t.fold(st, ip, request, status, bytesSent, referer, ua)
}

// fold aggregates one request.
func (t *TrafficSampler) fold(st *siteTrafficState, ip, request string, status int, bytesSent int64, referer, ua string) {
	// The panel's own requests to its sites (health checker, uptime probes)
	// are infrastructure, not site traffic. Counting them would poison every
	// feature: a quiet site polled once a minute looks like a single-IP
	// concentration attack and can never present a clean window.
	if strings.HasPrefix(ua, "EpicPanel-") {
		return
	}
	agg := &st.agg
	agg.Requests++
	agg.Bytes += bytesSent

	switch {
	case status >= 200 && status < 300:
		agg.Status2xx++
	case status >= 300 && status < 400:
		agg.Status3xx++
	case status >= 400 && status < 500:
		agg.Status4xx++
		if status == 404 {
			agg.NotFoundReqs++
		}
	case status >= 500:
		agg.Status5xx++
	}

	// request line: "METHOD path PROTO"
	method, path := "OTHER", ""
	if fields := strings.Fields(request); len(fields) >= 2 {
		method = fields[0]
		path = fields[1]
	} else if request != "" && !strings.Contains(request, " ") {
		// bare path (HTTP/0.9-style or garbage) — malformed request signal
		path = request
	}
	switch method {
	case "GET", "HEAD":
		agg.GetReqs++
	case "POST":
		agg.PostReqs++
	}

	if path != "" {
		agg.PathSamples++
		if len(st.paths) < t.maxPaths {
			if i := strings.IndexByte(path, '?'); i >= 0 {
				path = path[:i]
			}
			if _, seen := st.paths[path]; !seen {
				st.paths[path] = struct{}{}
				agg.UniquePaths++
			}
		}
	}

	if _, seen := st.ips[ip]; seen {
		st.ips[ip]++
	} else if len(st.ips) < t.maxIPs {
		st.ips[ip] = 1
		agg.UniqueIPs++
	} else {
		agg.TruncatedIPs++
	}
	if referer != "" && referer != "-" {
		agg.RefererReqs++
	}

	switch classifyUserAgent(ua) {
	case uaGoodBot:
		agg.UAGoodBot++
	case uaBadTool:
		agg.UABadTool++
	case uaHeadless:
		agg.UAHeadless++
	case uaEmpty:
		agg.UAEmpty++
	default:
		agg.UAOther++
	}
}

// uaClass buckets a user-agent string for the analyzer.
type uaClass int

const (
	uaOther uaClass = iota
	uaGoodBot
	uaBadTool
	uaHeadless
	uaEmpty
)

var goodBotSignatures = []string{
	"googlebot", "bingbot", "duckduckbot", "yandexbot", "baiduspider",
	"applebot", "facebookexternalhit", "twitterbot", "linkedinbot",
	"slackbot", "discordbot", "telegrambot", "whatsapp", "pinterestbot",
	"feedfetcher", "adsbot-google", "mediapartners-google",
}

// badToolSignatures: offensive/scanner tooling and generic HTTP libraries.
// Library UAs (curl/wget/...) are only abuse in volume — the analyzer weighs
// them; the classifier just buckets honestly.
var badToolSignatures = []string{
	"sqlmap", "nikto", "nessus", "openvas", "masscan", "zgrab", "zmap",
	"nmap", "dirbuster", "dirb", "gobuster", "wfuzz", "ffuf", "nuclei",
	"acunetix", "netsparker", "havij", "w3af", "hydra", "metasploit",
	"arachni", "whatweb", "wpscan", "joomscan",
	"curl", "wget", "python-requests", "python-urllib", "aiohttp",
	"go-http-client", "okhttp", "libwww-perl", "java/", "apache-httpclient",
	"scrapy", "node-fetch", "axios", "undici",
}

var headlessSignatures = []string{
	"headlesschrome", "phantomjs", "puppeteer", "playwright", "selenium",
}

func containsFold(list []string, s string) bool {
	for _, sig := range list {
		if strings.Contains(s, sig) {
			return true
		}
	}
	return false
}

// classifyUserAgent buckets a UA string.
func classifyUserAgent(ua string) uaClass {
	if ua == "" || ua == "-" {
		return uaEmpty
	}
	lower := strings.ToLower(ua)
	switch {
	case containsFold(goodBotSignatures, lower):
		return uaGoodBot
	case containsFold(badToolSignatures, lower):
		return uaBadTool
	case containsFold(headlessSignatures, lower):
		return uaHeadless
	default:
		return uaOther
	}
}
