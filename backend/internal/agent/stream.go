package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/epicbyte/epicpanel/backend/internal/agentproto"
	"github.com/gorilla/websocket"
)

// Streamer maintains the persistent agent → control-plane metrics stream.
// Responsibilities (protocol-metrics.md):
//   - high-frequency sample loop with self-throttling under load
//   - heartbeats on their own goroutine — never blocked by job execution
//     (the audit's head-of-line root cause)
//   - sequence numbers per session + ring buffer replay on reconnect
//   - exponential reconnect backoff
type Streamer struct {
	controlPlaneURL string
	token           string
	agentVersion    string
	base            time.Duration // configured cadence
	interval        time.Duration // current cadence (self-throttling)

	collector *Collector
	workloads *workloadCollector

	mu        sync.Mutex
	sessionID string
	seq       int64
	lastAcked int64
	ring      []agentproto.Frame
	ringHead  int
	startedAt time.Time

	connected bool

	// outCh carries asynchronous event frames produced by other subsystems.
	// It is the seam that keeps the producer side decoupled from the network
	// goroutine (spec §37).
	outCh chan agentproto.Frame
}

const ringSize = 256 // ~42 min of samples at 10s; replay horizon on reconnect

// NewStreamer wires the stream client.
func NewStreamer(controlPlaneURL, token, agentVersion string, interval time.Duration) *Streamer {
	if interval <= 0 {
		interval = intervalFromEnv()
	}
	return &Streamer{
		controlPlaneURL: controlPlaneURL,
		token:           token,
		agentVersion:    agentVersion,
		base:            interval,
		interval:        interval,
		collector:       NewCollector(),
		workloads:       newWorkloadCollector(),
		ring:            make([]agentproto.Frame, ringSize),
		outCh:           make(chan agentproto.Frame, 1024),
	}
}

// Send enqueues an asynchronous frame for delivery on the persistent stream.
// Non-blocking: when the queue is full the frame is dropped (event frames are
// best-effort; a producer that cannot keep up must never stall the sampling
// loop — spec §13, §52).
func (s *Streamer) Send(f agentproto.Frame) {
	select {
	case s.outCh <- f:
	default:
	}
}

// intervalFromEnv resolves the sample cadence (default 2s — live-feeling
// CPU/RAM/console stats without hammering the node).
func intervalFromEnv() time.Duration {
	if v := os.Getenv("EPICPANEL_AGENT_METRICS_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 2 * time.Second
}

// newSessionID returns a fresh random session identifier.
func newSessionID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// MetricsIntervalForLog surfaces the effective sample cadence for startup logs.
func MetricsIntervalForLog() time.Duration { return intervalFromEnv() }

// wsURL converts an http(s) control-plane base URL into its stream path.
func wsURL(base string) string {
	wsBase := strings.Replace(base, "http", "ws", 1)
	return strings.TrimSuffix(wsBase, "/") + "/v1/agent/stream"
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return h
}

func osInfo() string {
	b, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return "linux"
	}
	for _, line := range splitLines(b) {
		if strings.HasPrefix(line, "PRETTY_NAME=") {
			return strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), `"`)
		}
	}
	return "linux"
}

// Run blocks until ctx is done: connects, streams samples, reconnects with
// backoff. Intended as `go streamer.Run(ctx)`.
func (s *Streamer) Run(ctx context.Context) {
	s.mu.Lock()
	s.startedAt = time.Now().UTC()
	s.sessionID = newSessionID()
	s.ring = make([]agentproto.Frame, ringSize)
	s.mu.Unlock()

	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		err := s.streamOnce(ctx)
		s.setConnected(false)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			slog.Warn("metrics stream disconnected, will reconnect", "err", err, "backoff", backoff)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > 60*time.Second {
			backoff = 60 * time.Second
		}
	}
}

// Connected reports whether the stream is currently up (used by the
// heartbeat loop to skip redundant HTTP liveness).
func (s *Streamer) Connected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connected
}

func (s *Streamer) setConnected(v bool) {
	s.mu.Lock()
	s.connected = v
	s.mu.Unlock()
}

// streamOnce dials, handshakes (hello or resume), replays any frames the
// control plane missed, then samples until the connection drops.
func (s *Streamer) streamOnce(ctx context.Context) error {
	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	wsURL := wsURL(s.controlPlaneURL)
	header := http.Header{"Authorization": []string{"Bearer " + s.token}}
	conn, resp, err := websocket.DefaultDialer.DialContext(dialCtx, wsURL, header)
	if err != nil {
		if resp != nil {
			return fmt.Errorf("dial %s: %d", wsURL, resp.StatusCode)
		}
		return fmt.Errorf("dial %s: %w", wsURL, err)
	}
	defer conn.Close()
	s.setConnected(true)
	slog.Info("metrics stream connected")

	s.mu.Lock()
	sessionID, seq, lastAcked := s.sessionID, s.seq, s.lastAcked
	s.mu.Unlock()

	// Handshake: resume when this session already streamed frames, else hello.
	if lastAcked > 0 || seq > 0 {
		s.send(conn, agentproto.Frame{Type: agentproto.TypeResume, SessionID: sessionID, Seq: seq, Ts: time.Now().UTC(),
			Data: mustJSON(agentproto.Resume{LastSeq: lastAcked, Protocol: agentproto.ProtocolVersion})})
	} else {
		s.send(conn, agentproto.Frame{Type: agentproto.TypeHello, SessionID: sessionID, Seq: 0, Ts: time.Now().UTC(),
			Data: mustJSON(agentproto.Hello{
				Protocol:     agentproto.ProtocolVersion,
				AgentVersion: s.agentVersion,
				Hostname:     hostname(),
				OS:           osInfo(),
				StartedAt:    s.startedAt.Format(time.RFC3339),
			})})
	}

	errCh := make(chan error, 2)
	go func() {
		errCh <- s.readLoop(conn)
	}()

	// Replay buffered frames the control plane has not acked (resume only).
	s.replay(conn)

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.sendClose(conn)
			return ctx.Err()
		case err := <-errCh:
			return err
		case out := <-s.outCh:
			if err := s.send(conn, out); err != nil {
				return err
			}
		case <-ticker.C:
			frame, degraded, collectMS := s.nextSampleFrame()
			if err := s.send(conn, frame); err != nil {
				return err
			}
			if degraded {
				// Degrade VISIBLY: log every throttled pass.
				slog.Warn("metrics collection throttled under load",
					"collect_ms", collectMS, "interval", s.interval)
			}
		}
	}
}

// readLoop consumes control-plane frames: welcome/ack advance lastAcked,
// ping triggers pong. Exits on read error (dropped connection).
func (s *Streamer) readLoop(conn *websocket.Conn) error {
	conn.SetReadLimit(64 * 1024)
	for {
		var frame agentproto.Frame
		if err := conn.ReadJSON(&frame); err != nil {
			return err
		}
		switch frame.Type {
		case agentproto.TypeWelcome:
			s.mu.Lock()
			if frame.Data != nil {
				var w agentproto.Welcome
				if json.Unmarshal(frame.Data, &w) == nil && w.ResumeSeq > s.lastAcked {
					s.lastAcked = w.ResumeSeq
				}
			}
			s.mu.Unlock()
		case agentproto.TypeAck:
			s.mu.Lock()
			if frame.Data != nil {
				var a agentproto.Ack
				if json.Unmarshal(frame.Data, &a) == nil && a.Seq > s.lastAcked {
					s.lastAcked = a.Seq
				}
			}
			s.mu.Unlock()
		case agentproto.TypePing:
			s.send(conn, agentproto.Frame{Type: agentproto.TypePong, SessionID: s.sessionID, Seq: s.seq, Ts: time.Now().UTC()})
		}
	}
}

// replay resends frames with seq > lastAcked from the ring buffer (oldest
// first). Used only right after the resume handshake.
func (s *Streamer) replay(conn *websocket.Conn) {
	s.mu.Lock()
	resumeFrom := s.lastAcked
	if resumeFrom == 0 {
		s.mu.Unlock()
		return
	}
	var pending []agentproto.Frame
	for i := 0; i < ringSize; i++ {
		f := s.ring[(s.ringHead+i)%ringSize]
		if f.Seq > resumeFrom && f.Type == agentproto.TypeMetrics {
			pending = append(pending, f)
		}
	}
	s.mu.Unlock()
	for _, f := range pending {
		if err := s.send(conn, f); err != nil {
			return
		}
	}
	if len(pending) > 0 {
		slog.Info("replayed metrics frames after reconnect", "frames", len(pending), "from_seq", resumeFrom+1)
	}
}

// nextSampleFrame collects one batch, storing it in the ring buffer. The
// interval between passes is measured: if collection takes longer than the
// interval itself, the agent self-throttles (doubles the interval, capped)
// and reports degraded=true — degrade visibly, never silently.
func (s *Streamer) nextSampleFrame() (agentproto.Frame, bool, int64) {
	start := time.Now()
	node, _ := s.collector.Collect()
	sample := agentproto.Sample{Node: *node}
	sample.Sites = s.workloads.CollectSites()
	sample.Apps = s.workloads.CollectApps()
	sample.Containers = CollectContainers()
	collectMS := time.Since(start).Milliseconds()
	node.CollectMS = collectMS

	degraded := collectMS > int64(s.interval/time.Millisecond)
	if degraded {
		// Self-throttle: lengthen the interval rather than piling up.
		if s.interval < 30*time.Second {
			s.interval *= 2
		}
		node.Degraded = true
		node.DegradedReason = "collection overrun; interval doubled"
	} else if s.interval > s.base {
		// Recover gradually once the host is healthy again.
		s.interval /= 2
		if s.interval < s.base {
			s.interval = s.base
		}
	}

	s.mu.Lock()
	s.seq++
	frame := agentproto.Frame{
		Type:      agentproto.TypeMetrics,
		SessionID: s.sessionID,
		Seq:       s.seq,
		Ts:        time.Now().UTC(),
		Data:      mustJSON(sample),
	}
	s.ring[s.ringHead] = frame
	s.ringHead = (s.ringHead + 1) % ringSize
	s.mu.Unlock()
	return frame, degraded, collectMS
}

func (s *Streamer) send(conn *websocket.Conn, f agentproto.Frame) error {
	conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return conn.WriteJSON(f)
}

func (s *Streamer) sendClose(conn *websocket.Conn) {
	_ = conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, "agent shutdown"), time.Now().Add(2*time.Second))
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}
	return b
}
