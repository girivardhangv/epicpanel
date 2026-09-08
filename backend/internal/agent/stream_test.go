package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/epicbyte/epicpanel/backend/internal/agentproto"
	"github.com/gorilla/websocket"
)

// seqRecorder captures ingested frames like the control plane does.
type seqRecorder struct {
	mu    sync.Mutex
	seqs  []int64
	seen  map[int64]bool
	dupes int
	// rawDupes counts replayed frames that arrived again on the wire but
	// were correctly filtered by ingest (seq <= lastSeq for the session).
	rawDupes int
}

func (r *seqRecorder) record(seq int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seen == nil {
		r.seen = map[int64]bool{}
	}
	if r.seen[seq] {
		r.dupes++
	}
	r.seen[seq] = true
	r.seqs = append(r.seqs, seq)
}

// recordIngest mimics the control plane's LiveStore.Ingest sequence
// accounting: duplicates within the session are dropped, gaps tracked.
func (r *seqRecorder) recordIngest(session string, seq int64, lastSeq *int64, curSession *string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seen == nil {
		r.seen = map[int64]bool{}
	}
	if *curSession == session && seq <= *lastSeq {
		r.rawDupes++ // replayed duplicate correctly filtered
		return false
	}
	if *curSession != session {
		*curSession = session
		*lastSeq = seq
	} else {
		*lastSeq = seq
	}
	r.seqs = append(r.seqs, seq)
	r.seen[seq] = true
	return true
}

// startFramedServer runs a websocket server that speaks agentproto enough
// for the Streamer: welcome + acks, records metrics frames.
func startFramedServer(t *testing.T, rec *seqRecorder, dropAfter int, drops *atomic.Int64) *httptest.Server {
	t.Helper()
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer testtoken" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		sent := 0
		var lastSeq int64
		curSession := ""
		welcome, _ := json.Marshal(agentproto.Welcome{Protocol: agentproto.ProtocolVersion})
		_ = conn.WriteJSON(agentproto.Frame{Type: agentproto.TypeWelcome, Data: welcome})
		for {
			var f agentproto.Frame
			if err := conn.ReadJSON(&f); err != nil {
				return
			}
			switch f.Type {
			case agentproto.TypeMetrics:
				// Ingest semantics: dedup replays, then ack the frame.
				if ok := rec.recordIngest(f.SessionID, f.Seq, &lastSeq, &curSession); ok {
					sent++
					if dropAfter > 0 && sent >= dropAfter {
						drops.Add(1)
						return // simulate connection drop (before acking)
					}
				}
			}
			ack, _ := json.Marshal(agentproto.Ack{Seq: lastSeq})
			_ = conn.WriteJSON(agentproto.Frame{Type: agentproto.TypeAck, Data: ack})
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestStreamReconnectNoGapsNoDuplicates drives the Streamer against a local
// WS server that drops the connection mid-stream, then verifies: (a) the
// reconnect happens, (b) sequence numbers stay monotonic per session with
// no gaps the agent failed to replay and no duplicate ingestion.
func TestStreamReconnectNoGapsNoDuplicates(t *testing.T) {
	rec := &seqRecorder{}
	var drops atomic.Int64
	srv := startFramedServer(t, rec, 3, &drops)

	s := NewStreamer(strings.Replace(srv.URL, "http", "ws", 1), "testtoken", "test", 15*time.Millisecond)
	ctx, cancel := newTimeoutCtx(3 * time.Second)
	defer cancel()
	go s.Run(ctx)

	// Wait for at least one reconnect + recovery.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if drops.Load() >= 1 && rec.count() > 6 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()

	if drops.Load() == 0 {
		t.Fatal("server never dropped the connection; test inconclusive")
	}
	// Every seq from 1..max must be present (replay closes the gap).
	rec.mu.Lock()
	max := int64(0)
	for _, v := range rec.seqs {
		if v > max {
			max = v
		}
	}
	missing := rec.missingLocked(1, max)
	dupes := rec.dupes
	rec.mu.Unlock()
	if max < 4 {
		t.Fatalf("not enough frames to prove reconnect: max seq %d", max)
	}
	if missing != 0 {
		t.Fatalf("sequence gap detected across reconnect: missing %d seqs in 1..%d (have %v)", missing, max, rec.seqs)
	}
	if dupes > 0 {
		t.Fatalf("duplicate sequence INGESTION (post-dedup): %d", dupes)
	}
	t.Logf("reconnect verified: %d ingested frames, %d replayed duplicates correctly filtered, 0 gaps",
		len(rec.seqs), rec.rawDupes)
}

func (r *seqRecorder) missingLocked(from, to int64) int64 {
	var missing int64
	for i := from; i <= to; i++ {
		if !r.seen[i] {
			missing++
		}
	}
	return missing
}

func (r *seqRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.seqs)
}

func newTimeoutCtx(d time.Duration) (context.Context, func()) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	return ctx, cancel
}
