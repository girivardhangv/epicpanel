package agent

// ============================================================================
// Bot console (node agent side, Phase 8): a per-bot log ring buffer fed from
// journald (systemd is the supervisor — journald is where the process stdout
// goes), exposed to the control plane via the bot_logs job. Every line is
// scrubbed against the bot's secret values BEFORE it leaves the node — the
// control plane scrubs again at the API edge (defense in depth).
//
// There is NO command input path here: customers cannot send shell or docker
// commands to a bot. "Controlled input" is limited to the API-validated
// lifecycle jobs (start/stop/restart/kill) — nothing free-form exists.
// ============================================================================

import (
	"bufio"
	"context"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/epicbyte/epicpanel/backend/internal/discord"
)

// botRingSize is the per-bot console history kept in agent memory.
const botRingSize = 1000

type botRing struct {
	mu    sync.Mutex
	lines []discord.ConsoleLine
	seq   int64
	// since bounds the journald refill window (only lines newer than the
	// last refill are appended — job retries never duplicate entries).
	since time.Time
}

var (
	botRingsMu sync.Mutex
	botRings   = map[string]*botRing{}
)

// botSpec is the last-known running spec of a bot (env/git ciphertext) so
// log scrubbing works without a control-plane round trip.
type botSpec struct {
	envEnc      string
	gitTokenEnc string
}

var (
	botSpecsMu sync.Mutex
	botSpecs   = map[string]botSpec{}
)

// rememberBotSpec stores the scrub inputs when a bot starts.
func rememberBotSpec(botID, envEnc, gitTokenEnc string) {
	if envEnc == "" && gitTokenEnc == "" {
		return
	}
	botSpecsMu.Lock()
	botSpecs[botID] = botSpec{envEnc: envEnc, gitTokenEnc: gitTokenEnc}
	botSpecsMu.Unlock()
}

func botSpecOf(botID string) (botSpec, bool) {
	botSpecsMu.Lock()
	defer botSpecsMu.Unlock()
	s, ok := botSpecs[botID]
	return s, ok
}

func dropBotSpec(botID string) {
	botSpecsMu.Lock()
	delete(botSpecs, botID)
	botSpecsMu.Unlock()
	botRingsMu.Lock()
	delete(botRings, botID)
	botRingsMu.Unlock()
}

// botSecrets decrypts the stored scrub inputs ("" spec = no secrets known).
func botSecrets(botID string) []string {
	spec, ok := botSpecOf(botID)
	if !ok {
		return nil
	}
	vars, err := decryptEnvEnc(spec.envEnc)
	if err != nil {
		return nil
	}
	token, _ := decodeGitToken(spec.gitTokenEnc)
	return discord.SecretValues(vars, token)
}

func ringFor(botID string) *botRing {
	botRingsMu.Lock()
	defer botRingsMu.Unlock()
	r := botRings[botID]
	if r == nil {
		r = &botRing{}
		botRings[botID] = r
	}
	return r
}

// appendBotLines adds scrubbed lines to the ring and returns the first and
// last assigned seqs (0 lines = 0,0).
func appendBotLines(botID string, texts []string, secrets []string) (int64, int64) {
	r := ringFor(botID)
	r.mu.Lock()
	defer r.mu.Unlock()
	first := int64(0)
	last := r.seq
	for _, text := range texts {
		if text == "" {
			continue
		}
		if len(text) > 4096 {
			text = text[:4096]
		}
		r.seq++
		if first == 0 {
			first = r.seq
		}
		last = r.seq
		r.lines = append(r.lines, discord.ConsoleLine{
			Seq:  r.seq,
			Ts:   time.Now().UTC().Format(time.RFC3339),
			Text: discord.ScrubText(text, secrets),
		})
	}
	if len(r.lines) > botRingSize {
		r.lines = r.lines[len(r.lines)-botRingSize:]
	}
	return first, last
}

// botConsoleLines serves the bot_logs job: returns up to n recent lines
// (scrubbed). afterSeq > 0 returns only lines newer than that cursor.
func botConsoleLines(ctx context.Context, unit, botID string, n int, afterSeq int64, secrets []string) ([]discord.ConsoleLine, int64, error) {
	r := ringFor(botID)
	r.mu.Lock()
	needRefill := len(r.lines) == 0 || r.lines[len(r.lines)-1].Seq < afterSeq+int64(n)
	since := r.since
	r.mu.Unlock()

	if needRefill {
		texts, err := journalLinesSince(ctx, unit, botRingSize, since)
		if err == nil {
			// Overlap guard: the since-window can repeat the previous last
			// line; drop it before appending.
			r.mu.Lock()
			if len(r.lines) > 0 && len(texts) > 0 &&
				r.lines[len(r.lines)-1].Text == texts[0] {
				texts = texts[1:]
			}
			r.mu.Unlock()
			appendBotLines(botID, texts, secrets)
			r.mu.Lock()
			r.since = time.Now().Add(-time.Second)
			r.mu.Unlock()
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	var out []discord.ConsoleLine
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

// journalLinesSince reads stdout/stderr lines for the unit from journald
// (bounded output; no shell). Since zero → last n lines; otherwise the
// --since window bounds the read.
func journalLinesSince(ctx context.Context, unit string, n int, since time.Time) ([]string, error) {
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	args := []string{"-u", unit, "-n", strconv.Itoa(n), "--no-pager", "-o", "cat"}
	if !since.IsZero() {
		args = append(args, "--since", since.UTC().Format("2006-01-02 15:04:05"))
	}
	out, err := exec.CommandContext(c, "journalctl", args...).Output()
	if err != nil && len(out) == 0 {
		return nil, err
	}
	var lines []string
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	sc.Buffer(make([]byte, 64*1024), 64*1024)
	for sc.Scan() {
		if t := sc.Text(); t != "" {
			lines = append(lines, t)
		}
	}
	return lines, nil
}

// journalLines is the plain last-n read.
func journalLines(ctx context.Context, unit string, n int) ([]string, error) {
	return journalLinesSince(ctx, unit, n, time.Time{})
}

// BotConsoleFollowTail is the export the stream collector may use later to
// live-tail a unit (foundation; the bot_logs job path is the current wire).
func BotConsoleFollowTail(unit string) ([]string, error) {
	return journalLines(context.Background(), unit, 50)
}
