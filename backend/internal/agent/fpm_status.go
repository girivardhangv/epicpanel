package agent

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// ============================================================================
// Per-pool php-fpm status scraping.
//
// The dynamic resource engine needs worker-pool pressure (active workers vs
// pm.max_children, listen queue) — cgroup numbers alone cannot see a queue
// forming behind saturated workers. php-fpm exposes exactly this on its
// status endpoint, which we query DIRECTLY over each pool's unix socket
// with a minimal FastCGI client (no HTTP round trip through nginx).
//
// Pools are rendered with pm.status_path = /status (fpm.go); pools created
// before that line existed are self-healed once per agent process: the
// status path is appended, validated with php-fpm -t, and the service
// reloaded. A failed heal is retried on the next pass — degrade visibly
// (missing FPM pressure) never crashes the sampler.
// ============================================================================

const (
	fpmStatusTTL      = 5 * time.Second // matches the engine's FPM sampling cadence
	fpmScrapeTimeout  = 900 * time.Millisecond
	fpmStatusPathName = "/status"
)

// flexBool accepts php-fpm's "max children reached" as bool OR number —
// different PHP builds emit true/false or 1/0, and a strict bool made EVERY
// status decode fail silently (found live: all four pools reported zero
// workers because of this).
type flexBool bool

func (b *flexBool) UnmarshalJSON(p []byte) error {
	s := strings.TrimSpace(string(p))
	*b = s == "true" || s == "1"
	return nil
}

// FPMStatus is one pool's worker-pool snapshot (php-fpm status?json keys —
// the JSON field names contain spaces by design).
type FPMStatus struct {
	Active             int      `json:"active processes"`
	Idle               int      `json:"idle processes"`
	Total              int      `json:"total processes"`
	Queue              int      `json:"listen queue"`
	MaxChildrenReached flexBool `json:"max children reached"`
	// MaxChildren is NOT part of the status payload — it comes from the
	// pool file's pm.max_children (the enforcement denominator).
	MaxChildren int `json:"-"`
}

// fpmPool is one discovered pool: which site it serves and where to ask.
type fpmPool struct {
	WebsiteID   string
	UnixUser    string // the pool's `user =` directive — how SiteSamples are keyed
	Version     string // php major.minor (for the service name on self-heal)
	SocketPath  string
	MaxChildren int
	ConfPath    string
}

// FpmSample is one pool's status with BOTH identity keys: pool files are
// named by website UUID while cgroup SiteSamples are keyed by unix user —
// matching on either (stream.go) is what makes the telemetry arrive at all.
type FpmSample struct {
	WebsiteID string
	UnixUser  string
	Status    FPMStatus
}

// fpmScraper discovers epicpanel pools and scrapes their status, cached for
// fpmStatusTTL so the 2s metrics pass never pays the scrape every time.
type fpmScraper struct {
	mu      sync.Mutex
	cache   []FpmSample
	cacheAt time.Time
	healed  map[string]bool // self-heal attempted once per pool per process
}

func newFpmScraper() *fpmScraper {
	return &fpmScraper{healed: map[string]bool{}}
}

// Collect returns one sample per answering pool. Best-effort: pools that
// fail to respond (static site, pool down, socket gone) are simply absent.
func (f *fpmScraper) Collect() []FpmSample {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.cacheAt.IsZero() && time.Since(f.cacheAt) < fpmStatusTTL {
		return f.cache
	}
	var out []FpmSample
	changed := false
	for _, pool := range discoverFpmPools() {
		st, err := f.scrape(pool)
		if err != nil {
			// Missing status endpoint → heal once; other errors: skip.
			if errors.Is(err, errFpmNoStatus) && !f.healed[pool.WebsiteID] {
				f.healed[pool.WebsiteID] = true
				if healFpmStatusPath(pool) == nil {
					changed = true // reload landed — try again immediately
				}
			}
			continue
		}
		st.MaxChildren = pool.MaxChildren
		out = append(out, FpmSample{WebsiteID: pool.WebsiteID, UnixUser: pool.UnixUser, Status: st})
	}
	// A successful heal means the cache is stale the moment it was built.
	if changed {
		for _, pool := range discoverFpmPools() {
			if st, err := f.scrape(pool); err == nil {
				st.MaxChildren = pool.MaxChildren
				out = append(out, FpmSample{WebsiteID: pool.WebsiteID, UnixUser: pool.UnixUser, Status: st})
			}
		}
	}
	f.cache = out
	f.cacheAt = time.Now()
	return out
}

// discoverFpmPools globs every version's pool.d for epicpanel pools. The
// reserved dbadmin pool (not a website UUID) is skipped.
func discoverFpmPools() []fpmPool {
	matches, _ := filepath.Glob(phpEtcBase + "/*/fpm/pool.d/epicpanel-*.conf")
	pools := []fpmPool{}
	for _, conf := range matches {
		base := filepath.Base(conf)
		id := strings.TrimSuffix(strings.TrimPrefix(base, "epicpanel-"), ".conf")
		if !strings.Contains(id, "-") { // cheap UUID gate before Parse
			continue
		}
		if _, err := uuid.Parse(id); err != nil {
			continue
		}
		p := fpmPool{WebsiteID: id, ConfPath: conf}
		for _, seg := range strings.Split(filepath.ToSlash(conf), "/") {
			if strings.Count(seg, ".") == 1 && len(seg) <= 5 { // "8.3"
				p.Version = seg
				break
			}
		}
		socket, children, user := parsePoolRuntime(conf)
		p.SocketPath, p.MaxChildren, p.UnixUser = socket, children, user
		if p.SocketPath == "" || p.MaxChildren == 0 {
			continue
		}
		pools = append(pools, p)
	}
	return pools
}

// parsePoolRuntime pulls the lines the scraper needs from a pool file: the
// unix listen socket, pm.max_children, and the pool's unix user.
func parsePoolRuntime(conf string) (socket string, maxChildren int, unixUser string) {
	b, err := os.ReadFile(conf)
	if err != nil {
		return "", 0, ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case unixUser == "" && strings.HasPrefix(line, "user ="):
			unixUser = strings.TrimSpace(strings.TrimPrefix(line, "user ="))
		case strings.HasPrefix(line, "listen"):
			if v, ok := strings.CutPrefix(line, "listen ="); ok && strings.HasPrefix(strings.TrimSpace(v), "/") {
				socket = strings.TrimSpace(v)
			}
		case strings.HasPrefix(line, "pm.max_children ="):
			if v, ok := strings.CutPrefix(line, "pm.max_children ="); ok {
				fmt.Sscanf(strings.TrimSpace(v), "%d", &maxChildren)
			}
		}
	}
	return socket, maxChildren, unixUser
}

// scrape asks one pool for its status JSON over the FastCGI socket.
func (f *fpmScraper) scrape(p fpmPool) (FPMStatus, error) {
	var st FPMStatus
	conn, err := net.DialTimeout("unix", p.SocketPath, fpmScrapeTimeout/2)
	if err != nil {
		return st, fmt.Errorf("dial %s: %w", p.SocketPath, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(fpmScrapeTimeout))

	if err := writeFastCGIRequest(conn, fpmStatusPathName); err != nil {
		return st, err
	}
	body, err := readFastCGIResponse(conn)
	if err != nil {
		return st, err
	}
	// php-fpm answers status requests with a CGI-style header block; the
	// JSON payload starts at the first brace. Without pm.status_path the
	// master replies "File not found"/"Access denied" — healable.
	i := strings.Index(body, "{")
	if i < 0 {
		if strings.Contains(body, "File not found") || strings.Contains(body, "Access denied") {
			return st, errFpmNoStatus
		}
		return st, fmt.Errorf("unrecognized fpm status response: %.80q", body)
	}
	if err := json.Unmarshal([]byte(body[i:]), &st); err != nil {
		return st, fmt.Errorf("decode fpm status: %w", err)
	}
	return st, nil
}

var errFpmNoStatus = errors.New("fpm pool has no status endpoint")

// healFpmStatusPath appends pm.status_path to a legacy pool file, validates
// with php-fpm -t, reloads the service, and restores the file on any failure.
func healFpmStatusPath(p fpmPool) error {
	if p.Version == "" {
		return errors.New("cannot determine php version for pool")
	}
	b, err := os.ReadFile(p.ConfPath)
	if err != nil {
		return err
	}
	if strings.Contains(string(b), "pm.status_path") {
		return nil // present but FPM not reloaded yet — nothing to patch
	}
	backup := append([]byte(nil), b...)
	patched := string(b) + "\npm.status_path = " + fpmStatusPathName + "\n"
	if err := os.WriteFile(p.ConfPath, []byte(patched), 0o644); err != nil {
		return err
	}
	check := exec.Command(phpFpmBinary(p.Version), "--fpm-config", fpmMainConfig(p.Version), "--test")
	if out, err := check.CombinedOutput(); err != nil {
		_ = os.WriteFile(p.ConfPath, backup, 0o644)
		return fmt.Errorf("fpm config invalid after status_path patch: %v: %s", err, out)
	}
	// Reload (never restart) — same policy as enforcement's pool patches.
	if out, err := exec.Command("systemctl", "reload", phpFpmService(p.Version)).CombinedOutput(); err != nil {
		_ = os.WriteFile(p.ConfPath, backup, 0o644)
		_ = exec.Command("systemctl", "reload", phpFpmService(p.Version)).Run()
		return fmt.Errorf("reload %s: %v: %s", phpFpmService(p.Version), err, out)
	}
	return nil
}

// ============================================================================
// Minimal FastCGI (v1) client — just enough for one GET to php-fpm.
// Protocol: each record is an 8-byte header + content + padding.
// ============================================================================

const (
	fcgiVersion        = 1
	fcgiBeginRequest   = 1
	fcgiEndRequest     = 3
	fcgiParams         = 4
	fcgiStdin          = 5
	fcgiStdout         = 6
	fcgiResponderRole  = 1
	fcgiRequestID      = 1
)

func writeFastCGIRequest(conn net.Conn, scriptName string) error {
	// BEGIN_REQUEST: role (2B BE), flags, 5 reserved.
	begin := make([]byte, 8)
	binary.BigEndian.PutUint16(begin[0:2], fcgiResponderRole)
	if err := writeRecord(conn, fcgiBeginRequest, begin); err != nil {
		return err
	}
	params := map[string]string{
		"SCRIPT_NAME":     scriptName,
		"SCRIPT_FILENAME": scriptName,
		"REQUEST_METHOD":  "GET",
		"REQUEST_URI":     scriptName + "?json",
		"QUERY_STRING":    "json",
		"SERVER_PROTOCOL": "HTTP/1.0",
		"SERVER_SOFTWARE": "epicpanel-agent",
	}
	var pairs []byte
	for k, v := range params {
		pairs = appendNameValuePair(pairs, k, v)
	}
	if err := writeRecord(conn, fcgiParams, pairs); err != nil {
		return err
	}
	return writeRecord(conn, fcgiParams, nil) // end of params
}

func writeRecord(conn net.Conn, recType byte, content []byte) error {
	hdr := make([]byte, 8)
	hdr[0] = fcgiVersion
	hdr[1] = recType
	binary.BigEndian.PutUint16(hdr[2:4], fcgiRequestID)
	binary.BigEndian.PutUint16(hdr[4:6], uint16(len(content)))
	if _, err := conn.Write(hdr); err != nil {
		return err
	}
	if len(content) > 0 {
		if _, err := conn.Write(content); err != nil {
			return err
		}
	}
	return nil
}

func appendNameValuePair(dst []byte, k, v string) []byte {
	kb, vb := []byte(k), []byte(v)
	if len(kb) < 128 {
		dst = append(dst, byte(len(kb)))
	} else {
		dst = binary.BigEndian.AppendUint32(dst, uint32(len(kb))|0x80000000)
	}
	if len(vb) < 128 {
		dst = append(dst, byte(len(vb)))
	} else {
		dst = binary.BigEndian.AppendUint32(dst, uint32(len(vb))|0x80000000)
	}
	return append(append(dst, kb...), vb...)
}

func readFastCGIResponse(conn net.Conn) (string, error) {
	r := bufio.NewReader(conn)
	var out []byte
	for {
		hdr := make([]byte, 8)
		if _, err := ioReadFull(r, hdr); err != nil {
			return "", err
		}
		if hdr[0] != fcgiVersion {
			return "", fmt.Errorf("bad fastcgi version %d", hdr[0])
		}
		contentLen := binary.BigEndian.Uint16(hdr[4:6])
		padLen := hdr[6]
		content := make([]byte, int(contentLen)+int(padLen))
		if _, err := ioReadFull(r, content); err != nil {
			return "", err
		}
		switch hdr[1] {
		case fcgiStdout:
			out = append(out, content[:contentLen]...)
		case fcgiEndRequest:
			return string(out), nil
		}
	}
}

func ioReadFull(r *bufio.Reader, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := r.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}
