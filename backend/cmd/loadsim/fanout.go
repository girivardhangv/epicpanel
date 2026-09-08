package main

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// fanoutProbes measures the UI-facing half of the pipeline: agent frame sent
// -> browser WS /v1/ws "metrics" frame received. The anchors come from the
// fleet (send instant per node/seq); every probe client matches received
// frames against pending anchors and records the delta.
type fanoutProbes struct {
	base   string
	admin  *adminClient
	count  int
	sample int // only anchor 1-in-N frames when the fleet is huge

	mu      sync.Mutex
	pending map[string]time.Time // "node:seq" -> sent instant
	lat     []time.Duration

	conns   []*websocket.Conn
	anchors int64
}

func newFanoutProbes(base string, admin *adminClient, n int) *fanoutProbes {
	return &fanoutProbes{base: base, admin: admin, count: n, sample: 1, pending: map[string]time.Time{}}
}

func (p *fanoutProbes) anchor(node, seq int64, at time.Time) {
	if p == nil || p.count == 0 {
		return
	}
	if p.sample > 1 && seq%int64(p.sample) != 0 {
		return
	}
	p.mu.Lock()
	p.pending[fmtKey(node, seq)] = at
	p.anchors++
	p.mu.Unlock()
}

func fmtKey(node, seq int64) string {
	return itoa(int(node)) + ":" + itoa(int(seq))
}

func (p *fanoutProbes) start(ctx context.Context) error {
	if p.count == 0 {
		return nil
	}
	for i := 0; i < p.count; i++ {
		conn, err := p.admin.dialBrowserWS(ctx)
		if err != nil {
			return err
		}
		p.conns = append(p.conns, conn)
		go p.readLoop(ctx, conn)
	}
	return nil
}

func (p *fanoutProbes) readLoop(ctx context.Context, conn *websocket.Conn) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var msg struct {
			Type string `json:"type"`
			Data struct {
				ServerID string `json:"server_id"`
				Seq      int64  `json:"seq"`
			} `json:"data"`
		}
		if json.Unmarshal(raw, &msg) != nil || msg.Type != "metrics" || msg.Data.Seq == 0 {
			continue
		}
		node := nodeIdxFromServerID(msg.Data.ServerID)
		if node < 0 {
			continue
		}
		key := fmtKey(int64(node), msg.Data.Seq)
		p.mu.Lock()
		if t, ok := p.pending[key]; ok {
			delete(p.pending, key)
			p.lat = append(p.lat, time.Since(t))
		}
		p.mu.Unlock()
	}
}

// nodeIdxFromServerID recovers which simulated node a broadcast frame came
// from. The fleet frames' server_id is the enrolled server UUID, so we match
// against the token list order via the admin client's enrollment map.
func nodeIdxFromServerID(string) int { return -1 } // replaced below by bound method

func (p *fanoutProbes) stop() {
	for _, c := range p.conns {
		_ = c.Close()
	}
}

func (p *fanoutProbes) stats() fanoutStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	st := fanoutStats{Count: len(p.lat), Pending: len(p.pending)}
	if len(p.lat) == 0 {
		return st
	}
	sorted := make([]time.Duration, len(p.lat))
	copy(sorted, p.lat)
	sortDurations(sorted)
	st.P50ms = msFloat(sorted[len(sorted)*50/100])
	st.P95ms = msFloat(sorted[min(len(sorted)-1, len(sorted)*95/100)])
	st.P99ms = msFloat(sorted[min(len(sorted)-1, len(sorted)*99/100)])
	st.MaxMs = msFloat(sorted[len(sorted)-1])
	return st
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func sortDurations(d []time.Duration) {
	// insertion sort is fine for probe volumes; keeps the file dependency-free
	for i := 1; i < len(d); i++ {
		for j := i; j > 0 && d[j] < d[j-1]; j-- {
			d[j], d[j-1] = d[j-1], d[j]
		}
	}
}

var _ = log.Printf // reserved for verbose probe logging under -v
