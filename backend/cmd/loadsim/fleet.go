package main

import (
	"context"
	"sync"
	"time"

	"github.com/epicbyte/epicpanel/backend/internal/agentproto"
	"github.com/gorilla/websocket"
)

// fleet runs n simulated agents. Each one:
//   - dials GET /v1/agent/stream with its agent token (Bearer)
//   - sends hello, then metrics frames on the cadence, monotonically
//     increasing seq per session
//   - keeps a ring buffer of recent frames and answers a resume handshake
//     with a replay when reconnecting within the buffer window
//   - tracks sent / acked counters and per-frame send->ack latency
type fleet struct {
	base   string
	tokens []nodeToken
	sites  int
	every  time.Duration

	mu      sync.Mutex
	nodes   []*simNode
	stopped bool
}

type simNode struct {
	idx    int
	conn   *websocket.Conn
	seq    int64
	ring   []agentproto.Frame
	ringAt map[int64]time.Time // send timestamp per seq (for ack latency)

	acked   int64
	sent    int64
	latSum  time.Duration
	latMax  time.Duration
	bytes   int64
	errs    int
	resumes int
	lastAck time.Time

	cancel context.CancelFunc
}

func newFleet(base string, tokens []nodeToken, sites int, every time.Duration) *fleet {
	return &fleet{base: base, tokens: tokens, sites: sites, every: every}
}

func (f *fleet) start(ctx context.Context, fan *fanoutProbes) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nodes = make([]*simNode, len(f.tokens))
	for i := range f.tokens {
		nctx, cancel := context.WithCancel(ctx)
		n := &simNode{idx: i, ringAt: map[int64]time.Time{}}
		f.nodes[i] = n
		n.cancel = cancel
		if err := n.connect(nctx, f, true, fan); err != nil {
			// A failed initial connect must not kill the whole run: the
			// node retries with backoff inside connect.
			return err
		}
		go n.loop(nctx, f, fan)
	}
	return nil
}

// connect performs the stream handshake: dial, hello (or resume), welcome.
func (n *simNode) connect(ctx context.Context, f *fleet, first bool, fan *fanoutProbes) error {
	u := "ws" + trimSlash(f.base)[4:] + "/v1/agent/stream" // http->ws
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	hdr := map[string][]string{"Authorization": {"Bearer " + f.tokens[n.idx].AgentToken}}
	var conn *websocket.Conn
	var err error
	for attempt := 0; attempt < 30; attempt++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		conn, _, err = dialer.DialContext(ctx, u, hdr)
		if err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 200 * time.Millisecond):
		}
	}
	if err != nil {
		return err
	}
	n.conn = conn

	// hello on first connect; resume (last acked seq) on reconnect. The
	// control plane answers with welcome{resume_seq}; we replay frames with
	// seq > resume_seq from the ring (proves the sequence-continuity path).
	if first || n.lastAck.IsZero() {
		n.sendFrame(agentproto.Frame{
			Type: agentproto.TypeHello, SessionID: n.sessionID(), Ts: time.Now().UTC(),
			Data: mustJSON(agentproto.Hello{Protocol: agentproto.ProtocolVersion, AgentVersion: version, Hostname: f.tokens[n.idx].Hostname}),
		})
	} else {
		n.resumes++
		n.sendFrame(agentproto.Frame{
			Type: agentproto.TypeResume, SessionID: n.sessionID(), Ts: time.Now().UTC(),
			Data: mustJSON(agentproto.Resume{LastSeq: n.acked, Protocol: agentproto.ProtocolVersion}),
		})
		for _, fr := range n.ring {
			if fr.Seq > n.acked {
				n.sendFrame(fr)
			}
		}
	}

	// Wait for welcome (bounded) so the first metrics frame is not dropped.
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	var wf agentproto.Frame
	if err := conn.ReadJSON(&wf); err != nil {
		_ = conn.Close()
		return err
	}
	conn.SetReadDeadline(time.Time{})
	if wf.Type != agentproto.TypeWelcome {
		_ = conn.Close()
		return errStr("expected welcome, got " + wf.Type)
	}
	return nil
}

func (n *simNode) sessionID() string {
	// Stable per node for the process lifetime: resume replay is scoped to
	// one session.
	return "loadsim-sess-" + itoa(n.idx)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}

func (n *simNode) sendFrame(fr agentproto.Frame) {
	_ = n.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := n.conn.WriteJSON(fr); err == nil {
		n.sent++
	}
}

// loop is the per-node lifecycle: metrics cadence + ack/ping reader.
func (n *simNode) loop(ctx context.Context, f *fleet, fan *fanoutProbes) {
	tick := time.NewTicker(f.every)
	defer tick.Stop()
	defer func() {
		if n.conn != nil {
			_ = n.conn.Close()
		}
	}()

	acks := make(chan int64, 64)
	go func() {
		for {
			var fr agentproto.Frame
			if err := n.conn.ReadJSON(&fr); err != nil {
				close(acks)
				return
			}
			if fr.Type == agentproto.TypeAck {
				var a agentproto.Ack
				_ = jsonUnmarshal(fr.Data, &a)
				select {
				case acks <- a.Seq:
				default:
				}
			}
		}
	}()

	// Interleave ping responses: gorilla handles control frames
	// automatically inside ReadJSON, so no extra work is needed.

	for {
		select {
		case <-ctx.Done():
			return
		case seq, ok := <-acks:
			if !ok {
				return // connection dead; node intentionally stays down (chaos path)
			}
			now := time.Now()
			n.lastAck = now
			if t, known := n.ringAt[seq]; known {
				d := now.Sub(t)
				n.latSum += d
				if d > n.latMax {
					n.latMax = d
				}
			}
			n.acked = seq
		case <-tick.C:
			fr := f.buildFrame(n)
			n.seq++
			fr.Seq = n.seq
			n.ringAt[fr.Seq] = time.Now()
			n.ring = append(n.ring, fr)
			if len(n.ring) > 64 {
				delete(n.ringAt, n.ring[0].Seq)
				n.ring = n.ring[1:]
			}
			b, _ := jsonMarshal(fr)
			n.bytes += int64(len(b))
			// Fan-out latency anchor: record the send instant keyed by
			// (node, seq) so the browser probe can subtract it.
			if fan != nil {
				fan.anchor(int64(n.idx), fr.Seq, time.Now())
			}
			n.sendFrame(fr)
		}
	}
}

func (f *fleet) buildFrame(n *simNode) agentproto.Frame {
	ts := time.Now().UTC()
	sample := syntheticSample(n.idx, f.sites, ts)
	return agentproto.Frame{
		Type:      agentproto.TypeMetrics,
		SessionID: n.sessionID(),
		Ts:        ts,
		Data:      mustJSON(sample),
	}
}

// dropHard closes node i's stream and stops its loop (agent killed).
func (f *fleet) dropHard(i int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i < 0 || i >= len(f.nodes) || f.nodes[i] == nil {
		return
	}
	n := f.nodes[i]
	n.cancel()
	if n.conn != nil {
		_ = n.conn.Close()
	}
}

// resume reconnects node i from its ring buffer (agent restart within the
// retention window). Returns true when the stream is live again.
func (f *fleet) resume(i int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i < 0 || i >= len(f.nodes) || f.nodes[i] == nil {
		return false
	}
	n := f.nodes[i]
	if n.conn != nil {
		_ = n.conn.Close()
	}
	// A brand-new context governs the restarted loop.
	ctx, cancel := context.WithCancel(context.Background())
	n.cancel = cancel
	if err := n.connect(ctx, f, false, nil); err != nil {
		return false
	}
	go n.loop(ctx, f, nil)
	return true
}

func (f *fleet) stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, n := range f.nodes {
		if n != nil && n.cancel != nil {
			n.cancel()
		}
	}
}

func (f *fleet) resumedOK(i int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return i < len(f.nodes) && f.nodes[i] != nil && f.nodes[i].resumes > 0
}

func (f *fleet) stats() fleetStats {
	f.mu.Lock()
	defer f.mu.Unlock()
	var st fleetStats
	st.Nodes = len(f.nodes)
	var latSum time.Duration
	var latMax time.Duration
	for _, n := range f.nodes {
		if n == nil {
			continue
		}
		st.FramesSent += n.sent
		st.FramesAcked += n.acked
		latSum += n.latSum
		if n.latMax > latMax {
			latMax = n.latMax
		}
		st.BytesSent += n.bytes
		st.Errors += n.errs
		st.Resumes += n.resumes
	}
	if st.FramesAcked > 0 {
		st.AckLatAvgMS = msFloat(latSum / time.Duration(st.FramesAcked))
	}
	st.AckLatMaxMS = msFloat(latMax)
	return st
}
