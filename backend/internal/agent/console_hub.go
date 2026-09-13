package agent

import (
	"strings"
	"sync"
	"time"

	"github.com/epicbyte/epicpanel/backend/internal/agentproto"
)

// ============================================================================
// Console Hub (agent side) — the single owner of a workload's live output.
//
// One process-output stream (docker logs -f / journalctl -f) feeds this hub;
// the hub assigns a monotonic sequence to every line, keeps a bounded ring
// for replay, coalesces lines into batched frames and hands those frames to
// the stream emitter (the persistent panel WebSocket). Browsers never touch
// this layer: the panel fans one agent stream out to N browsers.
//
// Backpressure: emission is decoupled from reading. If the emitter cannot
// keep up, pending batches are dropped (the ring always has the recent
// truth) rather than blocking the game process — spec §13.
// ============================================================================

// consoleRingSize bounds per-workload history held in memory.
const consoleRingSize = 1000

// consoleBatchWindow is the coalescing latency bound; consoleBatchBytes is
// the size threshold that flushes immediately (low latency + low overhead).
const (
	consoleBatchWindow = 50 * time.Millisecond
	consoleBatchBytes  = 8 * 1024
)

type consoleLine struct {
	seq    int64
	stream string
	data   string
	ts     string
}

type consoleRing struct {
	mu    sync.Mutex
	lines []consoleLine
	seq   int64
}

type consoleBatch struct {
	firstSeq int64
	lastSeq  int64
	stream   string
	data     strings.Builder
}

// ConsoleHub multiplexes every workload's console on the node.
type ConsoleHub struct {
	mu    sync.Mutex
	rings map[string]*consoleRing
	pends map[string][]pendingChunk
	emit  func(agentproto.ConsoleOutput)

	once sync.Once
}

// MaxConsoleLineBytes caps a single console line (log-flood guard).
const MaxConsoleLineBytes = 8192

type pendingChunk struct {
	firstSeq int64
	lastSeq  int64
	stream   string
	data     string
}

var defaultConsoleHub = NewConsoleHub()

// NewConsoleHub builds an empty hub (exported for tests).
func NewConsoleHub() *ConsoleHub {
	return &ConsoleHub{
		rings: map[string]*consoleRing{},
		pends: map[string][]pendingChunk{},
	}
}

// SetEmitter installs the outbound frame sink (the streamer). The emitter
// MUST NOT block indefinitely — it is called from the hub's dispatcher.
func (h *ConsoleHub) SetEmitter(fn func(agentproto.ConsoleOutput)) {
	h.mu.Lock()
	h.emit = fn
	h.mu.Unlock()
}

// Start launches the dispatcher goroutine (idempotent).
func (h *ConsoleHub) Start() {
	h.once.Do(func() { go h.dispatch() })
}

// Append assigns sequence numbers to complete lines, appends them to the
// bounded ring and queues a batched outbound chunk. Lines are already
// scrubbed by the caller.
func (h *ConsoleHub) Append(workloadID, stream string, lines []string) {
	if workloadID == "" || len(lines) == 0 {
		return
	}
	if stream == "" {
		stream = agentproto.StreamStdout
	}
	now := time.Now().UTC().Format(time.RFC3339)
	r := h.ringFor(workloadID)

	r.mu.Lock()
	var chunk pendingChunk
	chunk.stream = stream
	for _, ln := range lines {
		ln = strings.TrimRight(ln, "\r")
		if ln == "" {
			continue
		}
		if len(ln) > MaxConsoleLineBytes {
			ln = ln[:MaxConsoleLineBytes]
		}
		r.seq++
		if chunk.firstSeq == 0 {
			chunk.firstSeq = r.seq
		}
		chunk.lastSeq = r.seq
		r.lines = append(r.lines, consoleLine{seq: r.seq, stream: stream, data: ln, ts: now})
		chunk.data += ln + "\n"
	}
	if len(r.lines) > consoleRingSize {
		r.lines = r.lines[len(r.lines)-consoleRingSize:]
	}
	r.mu.Unlock()

	if chunk.lastSeq == 0 {
		return
	}
	h.mu.Lock()
	h.pends[workloadID] = append(h.pends[workloadID], chunk)
	emit := h.emit
	h.mu.Unlock()
	// Emit immediately when the size threshold is crossed; otherwise the
	// dispatcher flushes on the small time window. No artificial delay.
	if emit != nil && len(chunk.data) >= consoleBatchBytes {
		h.flushOne(workloadID)
	}
}

// Seq returns the current last sequence of a workload's ring.
func (h *ConsoleHub) Seq(workloadID string) int64 {
	r := h.ring(workloadID)
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seq
}

// History returns the lines with seq > afterSeq (oldest first) plus the last
// assigned seq. A stale cursor (ring reset after restart) reads from the
// start instead of returning nothing.
func (h *ConsoleHub) History(workloadID string, afterSeq int64, n int) (lines []string, fromSeq, toSeq int64) {
	r := h.ring(workloadID)
	if r == nil {
		return nil, 0, 0
	}
	if n <= 0 || n > consoleRingSize {
		n = consoleRingSize
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if afterSeq > r.seq {
		afterSeq = 0
	}
	if afterSeq == 0 {
		start := len(r.lines) - n
		if start < 0 {
			start = 0
		}
		for _, l := range r.lines[start:] {
			lines = append(lines, l.data)
		}
		if len(r.lines) > 0 {
			fromSeq = r.lines[start].seq
			toSeq = r.seq
		}
		return lines, fromSeq, toSeq
	}
	for _, l := range r.lines {
		if l.seq > afterSeq {
			if fromSeq == 0 {
				fromSeq = l.seq
			}
			lines = append(lines, l.data)
		}
	}
	return lines, fromSeq, r.seq
}

// Reset clears a workload's console history while keeping the sequence
// MONOTONIC — the panel's cursor is keyed on seq, so resetting to zero would
// freeze the live console after a restart.
func (h *ConsoleHub) Reset(workloadID string) {
	r := h.ring(workloadID)
	if r == nil {
		return
	}
	r.mu.Lock()
	r.lines = nil
	r.mu.Unlock()
}

// Drop removes a workload's ring entirely (delete).
func (h *ConsoleHub) Drop(workloadID string) {
	h.mu.Lock()
	delete(h.rings, workloadID)
	delete(h.pends, workloadID)
	h.mu.Unlock()
}

func (h *ConsoleHub) ring(workloadID string) *consoleRing {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.rings[workloadID]
}

func (h *ConsoleHub) ringFor(workloadID string) *consoleRing {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.rings[workloadID]
	if r == nil {
		r = &consoleRing{}
		h.rings[workloadID] = r
	}
	return r
}

// dispatch flushes pending chunks on the coalescing window. One goroutine for
// the whole node: cheap, and never in the read path.
func (h *ConsoleHub) dispatch() {
	ticker := time.NewTicker(consoleBatchWindow)
	defer ticker.Stop()
	for range ticker.C {
		h.flushAll()
	}
}

func (h *ConsoleHub) flushAll() {
	h.mu.Lock()
	ids := make([]string, 0, len(h.pends))
	for id, q := range h.pends {
		if len(q) > 0 {
			ids = append(ids, id)
		}
	}
	h.mu.Unlock()
	for _, id := range ids {
		h.flushOne(id)
	}
}

// flushOne coalesces every pending chunk for a workload into one frame. A
// slow emitter is handled by dropping the batch (Option A) — the ring keeps
// the recent output for replay, so nothing is lost for a reconnecting panel.
func (h *ConsoleHub) flushOne(workloadID string) {
	h.mu.Lock()
	q := h.pends[workloadID]
	if len(q) == 0 {
		h.mu.Unlock()
		return
	}
	delete(h.pends, workloadID)
	emit := h.emit
	h.mu.Unlock()

	batch := consoleBatch{stream: agentproto.StreamStdout}
	for _, c := range q {
		if batch.firstSeq == 0 {
			batch.firstSeq = c.firstSeq
		}
		batch.lastSeq = c.lastSeq
		batch.data.WriteString(c.data)
	}
	if emit == nil || batch.lastSeq == 0 {
		return
	}
	frame := agentproto.ConsoleOutput{
		ServerID: workloadID,
		FirstSeq: batch.firstSeq,
		Seq:      batch.lastSeq,
		Stream:   batch.stream,
		Data:     batch.data.String(),
		Ts:       time.Now().UTC().Format(time.RFC3339),
	}
	emit(frame)
}

// DefaultConsoleHub exposes the process-wide hub to the ops and streamer.
func DefaultConsoleHub() *ConsoleHub { return defaultConsoleHub }
