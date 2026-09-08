package agent

// ============================================================================
// Minecraft console (node agent side, Phase 7): a per-instance log ring
// buffer fed from journald (systemd is the supervisor — journald is where
// the process stdout goes), exposed to the control plane via the mc_logs
// job. Every line is scrubbed against the instance's RCON password BEFORE
// it leaves the node — the control plane scrubs again at the API edge
// (defense in depth).
//
// Command input does NOT live here: customers send allowlisted console
// commands through mc_command (REST → job → RCON). There is no shell and
// no docker anywhere on this path.
// ============================================================================

import (
	"bufio"
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/epicbyte/epicpanel/backend/internal/minecraft"
)

// mcRingSize is the per-instance console history kept in agent memory.
const mcRingSize = 1000

type mcRing struct {
	mu    sync.Mutex
	lines []minecraft.ConsoleLine
	seq   int64
	// since bounds the journald refill window (only lines newer than the
	// last refill are appended — job retries never duplicate entries).
	since time.Time
}

var (
	mcRingsMu sync.Mutex
	mcRings   = map[string]*mcRing{}
)

// mcSpec is the last-known running spec of an instance (rcon inputs) so log
// scrubbing and metrics polling work without a control-plane round trip.
type mcSpec struct {
	rconPass    string
	rconPort    int
	supportsTPS bool
}

var (
	mcSpecsMu sync.Mutex
	mcSpecs   = map[string]mcSpec{}
)

// RememberMCSpec stores the scrub/poll inputs when an instance starts.
func RememberMCSpec(instanceID, rconPass string, rconPort int, supportsTPS bool) {
	if rconPass == "" && rconPort == 0 {
		return
	}
	mcSpecsMu.Lock()
	mcSpecs[instanceID] = mcSpec{rconPass: rconPass, rconPort: rconPort, supportsTPS: supportsTPS}
	mcSpecsMu.Unlock()
}

func mcSpecOf(instanceID string) (mcSpec, bool) {
	mcSpecsMu.Lock()
	defer mcSpecsMu.Unlock()
	s, ok := mcSpecs[instanceID]
	return s, ok
}

// ForgetMCSpec drops the spec + ring for a deleted instance.
func ForgetMCSpec(instanceID string) {
	mcSpecsMu.Lock()
	delete(mcSpecs, instanceID)
	mcSpecsMu.Unlock()
	mcRingsMu.Lock()
	delete(mcRings, instanceID)
	mcRingsMu.Unlock()
}

// mcSecrets decrypts the stored scrub inputs ("" spec = no secrets known).
func mcSecrets(instanceID string) []string {
	spec, ok := mcSpecOf(instanceID)
	if !ok {
		return nil
	}
	return minecraft.SecretValues(spec.rconPass)
}

func mcRingFor(instanceID string) *mcRing {
	mcRingsMu.Lock()
	defer mcRingsMu.Unlock()
	r := mcRings[instanceID]
	if r == nil {
		r = &mcRing{}
		mcRings[instanceID] = r
	}
	return r
}

// appendMCLines adds scrubbed lines to the ring.
func appendMCLines(instanceID string, texts []string, secrets []string) {
	r := mcRingFor(instanceID)
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, text := range texts {
		if text == "" {
			continue
		}
		if len(text) > 4096 {
			text = text[:4096]
		}
		r.seq++
		r.lines = append(r.lines, minecraft.ConsoleLine{
			Seq:  r.seq,
			Ts:   time.Now().UTC().Format(time.RFC3339),
			Text: minecraft.ScrubText(text, secrets),
		})
	}
	if len(r.lines) > mcRingSize {
		r.lines = r.lines[len(r.lines)-mcRingSize:]
	}
}

// mcConsoleLines serves the mc_logs job: returns up to n recent lines
// (scrubbed). afterSeq > 0 returns only lines newer than that cursor.
func mcConsoleLines(ctx context.Context, unit, instanceID string, n int, afterSeq int64, secrets []string) ([]minecraft.ConsoleLine, int64, error) {
	r := mcRingFor(instanceID)
	r.mu.Lock()
	needRefill := len(r.lines) == 0 || r.lines[len(r.lines)-1].Seq < afterSeq+int64(n)
	since := r.since
	r.mu.Unlock()

	if needRefill {
		texts, err := journalLinesSince(ctx, unit, mcRingSize, since)
		if err == nil {
			// Overlap guard: the since-window can repeat the previous last
			// line; drop it before appending.
			r.mu.Lock()
			if len(r.lines) > 0 && len(texts) > 0 &&
				r.lines[len(r.lines)-1].Text == texts[0] {
				texts = texts[1:]
			}
			r.mu.Unlock()
			appendMCLines(instanceID, texts, secrets)
			r.mu.Lock()
			r.since = time.Now().Add(-time.Second)
			r.mu.Unlock()
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	var out []minecraft.ConsoleLine
	if afterSeq > 0 {
		for _, l := range r.lines {
			if l.Seq > afterSeq {
				out = append(out, l)
			}
		}
	} else {
		start := len(r.lines) - n
		if start < 0 {
			start = 0
		}
		out = append(out, r.lines[start:]...)
	}
	if len(r.lines) == 0 {
		return out, afterSeq, nil
	}
	return out, r.lines[len(r.lines)-1].Seq, nil
}

// mcJournalLinesSince is journalLinesSince (re-exported alias for symmetry
// with the bot console naming).
func mcJournalLinesSince(ctx context.Context, unit string, n int, since time.Time) ([]string, error) {
	return journalLinesSince(ctx, unit, n, since)
}

// ============================================================================
// Safe shutdown: save-all → stop (graceful) → wait → SIGTERM → SIGKILL.
// The control plane never needs to know the details — the outcome carries
// the honest path taken.
// ============================================================================

// MCStopGrace is how long a graceful `stop` gets before escalation.
const MCStopGrace = 30 * time.Second

// mcSafeShutdown performs the verbatim sequence for one unit. Idempotent:
// an already-stopped unit is a no-op success.
func mcSafeShutdown(ctx context.Context, unit, instanceID string) string {
	var log strings.Builder
	if t, err := defaultDriver.Status(ctx, unit); err == nil && t.State != "active" && t.State != "activating" {
		StopMCMetricsPoller(instanceID)
		_ = defaultDriver.Stop(ctx, unit)
		return "already stopped"
	}

	// 1. save-all + stop over RCON (graceful; the server flushes the world).
	if spec, ok := mcSpecOf(instanceID); ok && spec.rconPort > 0 && spec.rconPass != "" {
		addr := "127.0.0.1:" + strconv.Itoa(spec.rconPort)
		if conn, err := minecraft.DialRCON(addr, spec.rconPass, 5*time.Second); err == nil {
			if _, cerr := conn.Command("save-all"); cerr == nil {
				log.WriteString("save-all ok; ")
			}
			_, _ = conn.Command("stop")
			_ = conn.Close()
			log.WriteString("stop sent; ")
		} else {
			log.WriteString("rcon unreachable; ")
		}
	}

	// 2. wait for graceful exit.
	deadline := time.Now().Add(MCStopGrace)
	for time.Now().Before(deadline) {
		if t, err := defaultDriver.Status(ctx, unit); err == nil && t.State != "active" && t.State != "deactivating" {
			log.WriteString("stopped gracefully")
			StopMCMetricsPoller(instanceID)
			_ = defaultDriver.Stop(ctx, unit)
			return strings.TrimSuffix(log.String(), "; ")
		}
		select {
		case <-ctx.Done():
			deadline = time.Now() // skip to escalation on cancel
		case <-time.After(500 * time.Millisecond):
		}
	}

	// 3. escalation: SIGTERM (systemd stop) → short wait → SIGKILL.
	log.WriteString("grace window elapsed; escalating; ")
	_ = defaultDriver.Stop(ctx, unit)
	time.Sleep(3 * time.Second)
	if t, err := defaultDriver.Status(ctx, unit); err == nil && t.State == "active" {
		log.WriteString("SIGKILL; ")
		_ = defaultDriver.Kill(ctx, unit)
	}
	StopMCMetricsPoller(instanceID)
	return strings.TrimSuffix(strings.TrimSpace(log.String()), ";")
}

// ============================================================================
// Metrics poller: RCON list/tps/paper mspt → the AppSample minecraft fields
// (Players/TPS/MSPT) for the Phase 3 envelope, with honesty flags (a value
// the server does not expose stays zero; the agent-side cache records the
// gap so the control plane can label it).
// ============================================================================

type mcMetricsCache struct {
	mu        sync.Mutex
	players   int
	tps       float64
	mspt      float64
	tpsKnown  bool
	msptKnown bool
	listKnown bool
	at        time.Time
}

var (
	mcMetricsMu     sync.Mutex
	mcMetricsCacheM = map[string]*mcMetricsCache{}
	mcPollersMu     sync.Mutex
	mcPollers       = map[string]chan struct{}{}
)

func mcMetricsFor(instanceID string) *mcMetricsCache {
	mcMetricsMu.Lock()
	defer mcMetricsMu.Unlock()
	c := mcMetricsCacheM[instanceID]
	if c == nil {
		c = &mcMetricsCache{}
		mcMetricsCacheM[instanceID] = c
	}
	return c
}

// MCPollMetrics returns the last cached metrics snapshot + what is known.
func MCPollMetrics(ctx context.Context, instanceID string) (players, tps, mspt float64, known struct {
	list, tps, mspt bool
}) {
	c := mcMetricsFor(instanceID)
	c.mu.Lock()
	defer c.mu.Unlock()
	return float64(c.players), c.tps, c.mspt, struct{ list, tps, mspt bool }{c.listKnown, c.tpsKnown, c.msptKnown}
}

// StartMCMetricsPoller launches (or refreshes) the per-instance RCON poller
// (15s cadence — metrics stream at the envelope cadence, RCON stays cheap).
// Idempotent: an existing poller is left running.
func StartMCMetricsPoller(instanceID string) {
	mcPollersMu.Lock()
	if _, running := mcPollers[instanceID]; running {
		mcPollersMu.Unlock()
		return
	}
	stop := make(chan struct{})
	mcPollers[instanceID] = stop
	mcPollersMu.Unlock()

	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		pollOnce(instanceID) // immediate first poll (may fail until boot)
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				pollOnce(instanceID)
			}
		}
	}()
}

// StopMCMetricsPoller cancels the poller (stop signal only — no goroutine
// leaks; the loop exits on the next tick).
func StopMCMetricsPoller(instanceID string) {
	mcPollersMu.Lock()
	if stop, ok := mcPollers[instanceID]; ok {
		close(stop)
		delete(mcPollers, instanceID)
	}
	mcPollersMu.Unlock()
}

// pollOnce gathers players + TPS/MSPT over RCON and caches them honestly.
func pollOnce(instanceID string) {
	spec, ok := mcSpecOf(instanceID)
	if !ok || spec.rconPort == 0 || spec.rconPass == "" {
		return
	}
	addr := "127.0.0.1:" + strconv.Itoa(spec.rconPort)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	_ = ctx
	conn, err := minecraft.DialRCON(addr, spec.rconPass, 5*time.Second)
	if err != nil {
		c := mcMetricsFor(instanceID)
		c.mu.Lock()
		c.listKnown = false
		c.at = time.Now()
		c.mu.Unlock()
		return
	}
	defer conn.Close()

	c := mcMetricsFor(instanceID)
	c.mu.Lock()
	c.listKnown = true
	c.at = time.Now()
	c.mu.Unlock()

	if resp, err := conn.Command("list"); err == nil {
		if online, _, _, ok := minecraft.RCONPlayers(resp); ok {
			c.mu.Lock()
			c.players = online
			c.mu.Unlock()
		}
	}
	if spec.supportsTPS {
		if resp, err := conn.Command("tps"); err == nil {
			if v, ok := minecraft.RCONTPS(resp); ok {
				c.mu.Lock()
				c.tps = v
				c.tpsKnown = true
				c.mu.Unlock()
			}
		}
		if resp, err := conn.Command("paper mspt"); err == nil {
			if v, ok := minecraft.RCONMSPT(resp); ok {
				c.mu.Lock()
				c.mspt = v
				c.msptKnown = true
				c.mu.Unlock()
			}
		}
	}
	// Also surface command output on the console ring (scrubbed).
	secrets := mcSecrets(instanceID)
	var texts []string
	if resp, err := conn.Command("list"); err == nil && strings.TrimSpace(resp) != "" {
		texts = append(texts, strings.TrimSpace(resp))
	}
	if len(texts) > 0 {
		appendMCLines(instanceID, texts, secrets)
	}
}

// mcMetricsSnapshotView is exported for the AppSample assembly (collector).
type mcMetricsSnapshotView struct {
	Players   int
	TPS       float64
	MSPT      float64
	TPSKnown  bool
	MSPTKnown bool
	ListKnown bool
	At        time.Time
}

// MCMetricsSnapshot returns a copy of the cached snapshot.
func MCMetricsSnapshot(instanceID string) mcMetricsSnapshotView {
	c := mcMetricsFor(instanceID)
	c.mu.Lock()
	defer c.mu.Unlock()
	return mcMetricsSnapshotView{
		Players: c.players, TPS: c.tps, MSPT: c.mspt,
		TPSKnown: c.tpsKnown, MSPTKnown: c.msptKnown, ListKnown: c.listKnown, At: c.at,
	}
}

// mcScanner helper kept for potential multi-line RCON reads.
func mcScannerLines(s string) []string {
	var out []string
	sc := bufio.NewScanner(strings.NewReader(s))
	sc.Buffer(make([]byte, 64*1024), 64*1024)
	for sc.Scan() {
		if t := sc.Text(); t != "" {
			out = append(out, t)
		}
	}
	return out
}
