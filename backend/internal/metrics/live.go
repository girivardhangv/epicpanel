// Package metrics implements the Phase 3 real-time pipeline on the control
// plane: agent stream ingest → instantaneous in-memory store (LIVE / STALE /
// OFFLINE with age) → WebSocket fan-out, plus an async batched historical
// writer that never sits in the live path.
package metrics

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/agentproto"
)

func jsonMarshal(v any) ([]byte, error)   { return json.Marshal(v) }
func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
func unmarshalSample(data json.RawMessage, out *agentproto.Sample) error {
	return json.Unmarshal(data, out)
}

// ringPerServer keeps recent samples for instantaneous sparklines WITHOUT
// touching the historical database (master doc: the live dashboard must not
// read the history store).
const ringPerServer = 180

// NodeState is the node connection registry state.
const (
	NodeOnline  = "ONLINE"
	NodeStale   = "STALE"
	NodeOffline = "OFFLINE"
)

// ServerLive is the in-memory live view of one node.
type ServerLive struct {
	ServerID   uuid.UUID
	SessionID  string
	Connected  bool
	LastFrame  time.Time // last metrics/heartbeat frame ingested
	LastSeq    int64
	RecvCount  int64
	Gaps       int64 // seq jumps > 1 within a session (never silently ignored)
	Degraded   bool
	DegradedBy string

	// Latest instantaneous sample + its ring buffer.
	Sample    *agentproto.Sample
	Collected time.Time
	Ring      []RingPoint

	Sites      map[string]agentproto.SiteSample
	Containers []agentproto.ContainerSample
	Apps       []agentproto.AppSample
}

// RingPoint is one retained instantaneous sample (recent window only).
type RingPoint struct {
	At      time.Time `json:"at"`
	CPU     float64   `json:"cpu"`
	MemPct  float64   `json:"mem_pct"`
	Load1   float64   `json:"load1"`
	RxBPS   float64   `json:"rx_bps"`
	TxBPS   float64   `json:"tx_bps"`
	Degrade bool      `json:"degraded,omitempty"`
}

// Freshness classifies the latest sample per the protocol contract.
func (s *ServerLive) Freshness(now time.Time) map[string]any {
	ageMs := int64(-1)
	have := s.Sample != nil
	state := agentproto.Freshness(now.Sub(s.Collected), have)
	if have {
		ageMs = now.Sub(s.Collected).Milliseconds()
	}
	return map[string]any{"state": state, "age_ms": ageMs}
}

// NodeState derives the connection-registry state from the last frame age
// and connection status. Thresholds live in one place (protocol-metrics.md):
// ONLINE < 30s since last frame · STALE 30–120s · OFFLINE > 120s.
func (s *ServerLive) NodeState(now time.Time) string {
	if !s.Connected {
		if s.LastFrame.IsZero() {
			return NodeOffline
		}
	}
	age := now.Sub(s.LastFrame)
	if s.Connected {
		if age <= 30*time.Second {
			return NodeOnline
		}
		if age <= agentproto.StaleMaxAge {
			return NodeStale
		}
		return NodeOffline
	}
	// Connection down: stay STALE while frames could still be arriving
	// through the legacy HTTP heartbeat, OFFLINE after that.
	switch {
	case age <= 30*time.Second:
		return NodeStale
	case age <= agentproto.StaleMaxAge:
		return NodeStale
	default:
		return NodeOffline
	}
}

// LiveStore is the authoritative instantaneous metric store (in-memory).
// It is sharded by server and safe for concurrent ingest + API reads.
type LiveStore struct {
	mu      sync.RWMutex
	servers map[uuid.UUID]*ServerLive

	// OnSample fans each ingested frame out to the WebSocket hub. Set by
	// the api wiring; must not block.
	OnSample func(serverID uuid.UUID, frame map[string]any)
	// OnTraffic receives completed per-site traffic windows (dynamic
	// resources feature). Called while the store lock is held with a fresh
	// slice — must not block or re-lock the LiveStore.
	OnTraffic func(frames []agentproto.SiteTraffic)
}

func NewLiveStore() *LiveStore {
	return &LiveStore{servers: map[uuid.UUID]*ServerLive{}}
}

// Connected marks an agent stream connection up (or down on disconnect).
func (s *LiveStore) Connected(serverID uuid.UUID, sessionID string, up bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	lv := s.servers[serverID]
	if lv == nil {
		lv = &ServerLive{ServerID: serverID}
		s.servers[serverID] = lv
	}
	if up {
		lv.Connected = true
		lv.SessionID = sessionID
		lv.LastFrame = time.Now().UTC()
	} else {
		lv.Connected = false
	}
}

// Ingest applies one frame: dedups/gap-tracks by seq, stores the sample,
// and (for metrics frames) fans out. Returns false when the frame was a
// duplicate (already-ingested seq within the session).
func (s *LiveStore) Ingest(serverID uuid.UUID, frame agentproto.Frame) bool {
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()

	lv := s.servers[serverID]
	if lv == nil {
		lv = &ServerLive{ServerID: serverID}
		s.servers[serverID] = lv
	}
	lv.LastFrame = now

	switch frame.Type {
	case agentproto.TypeHello:
		lv.Connected = true
		if lv.SessionID != frame.SessionID {
			lv.SessionID = frame.SessionID
			lv.LastSeq = 0 // new session: sequence restarts
		}
		return true
	case agentproto.TypeResume:
		if lv.SessionID != frame.SessionID {
			lv.SessionID = frame.SessionID
			lv.LastSeq = 0
		}
		return true
	case agentproto.TypeHeartbeat:
		lv.Connected = true
		return true
	case agentproto.TypeMetrics:
		lv.Connected = true
	}

	// Sequence accounting (only meaningful within one session).
	if frame.SessionID != "" && frame.SessionID == lv.SessionID {
		if frame.Seq <= lv.LastSeq {
			return false // duplicate (replayed frame already ingested)
		}
		if lv.LastSeq > 0 && frame.Seq > lv.LastSeq+1 {
			lv.Gaps += frame.Seq - lv.LastSeq - 1
		}
	} else if frame.SessionID != "" {
		lv.SessionID = frame.SessionID
		lv.LastSeq = frame.Seq
	}
	lv.LastSeq = frame.Seq
	lv.RecvCount++

	var sample agentproto.Sample
	if err := unmarshalSample(frame.Data, &sample); err != nil {
		return true // non-metrics data shape: keep liveness, drop payload
	}
	collected := frame.Ts
	if collected.IsZero() {
		collected = now
	}
	lv.Sample = &sample
	lv.Collected = collected
	lv.Degraded = sample.Node.Degraded
	lv.DegradedBy = sample.Node.DegradedReason
	lv.Sites = make(map[string]agentproto.SiteSample, len(sample.Sites))
	for _, site := range sample.Sites {
		lv.Sites[site.WebsiteID] = site
	}
	lv.Containers = sample.Containers
	lv.Apps = sample.Apps
	if len(sample.Traffic) > 0 && s.OnTraffic != nil {
		s.OnTraffic(sample.Traffic)
	}

	memPct := 0.0
	if sample.Node.MemoryTotal > 0 {
		memPct = float64(sample.Node.MemoryUsed) / float64(sample.Node.MemoryTotal) * 100
	}
	lv.Ring = append(lv.Ring, RingPoint{
		At: collected, CPU: sample.Node.CPUPercent, MemPct: memPct,
		Load1: sample.Node.Load1, RxBPS: sample.Node.Net.RxBPS, TxBPS: sample.Node.Net.TxBPS,
		Degrade: sample.Node.Degraded,
	})
	if len(lv.Ring) > ringPerServer {
		lv.Ring = lv.Ring[len(lv.Ring)-ringPerServer:]
	}

	if s.OnSample != nil {
		s.OnSample(serverID, s.snapshotFrame(serverID, lv, now))
	}
	return true
}

// Snapshot returns the live view for one server (nil when no data ever).
func (s *LiveStore) Snapshot(serverID uuid.UUID) *ServerLive {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.servers[serverID]
}

// SnapshotFrame is the JSON projection served over REST/WS.
type SnapshotFrame struct {
	ServerID       string                       `json:"server_id"`
	SessionID      string                       `json:"session_id,omitempty"`
	Seq            int64                        `json:"seq"`
	CollectedAt    time.Time                    `json:"collected_at"`
	ReceivedAt     time.Time                    `json:"received_at"`
	Freshness      map[string]any               `json:"freshness"`
	NodeState      string                       `json:"node_state"`
	Degraded       bool                         `json:"degraded"`
	DegradedReason string                       `json:"degraded_reason,omitempty"`
	Connected      bool                         `json:"connected"`
	Sample         *agentproto.Sample           `json:"sample"`
	Ring           []RingPoint                  `json:"ring,omitempty"`
	Sites          []agentproto.SiteSample      `json:"sites,omitempty"`
	Containers     []agentproto.ContainerSample `json:"containers,omitempty"`
	Apps           []agentproto.AppSample       `json:"apps,omitempty"`
}

// Frame builds the broadcast/snapshot projection for one server.
func (s *LiveStore) Frame(serverID uuid.UUID) *SnapshotFrame {
	now := time.Now().UTC()
	s.mu.RLock()
	defer s.mu.RUnlock()
	lv := s.servers[serverID]
	if lv == nil {
		return nil
	}
	return snapshotOf(serverID, lv, now)
}

// Frames returns projections for every known server.
func (s *LiveStore) Frames() []SnapshotFrame {
	now := time.Now().UTC()
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]SnapshotFrame, 0, len(s.servers))
	for id, lv := range s.servers {
		out = append(out, *snapshotOf(id, lv, now))
	}
	return out
}

// HistoryBatches drains all current samples for the historical writer
// (non-destructive; the writer throttles itself via its own cadence).
func (s *LiveStore) HistoryBatches() []HistoryRow {
	now := time.Now().UTC()
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []HistoryRow
	for id, lv := range s.servers {
		if lv.Sample == nil {
			continue
		}
		if now.Sub(lv.Collected) > agentproto.LiveMaxAge*2 {
			continue // too old to double-persist; the writer saw it live
		}
		out = append(out, HistoryRow{ServerID: id, Sample: lv.Sample, At: lv.Collected})
	}
	return out
}

func snapshotOf(id uuid.UUID, lv *ServerLive, now time.Time) *SnapshotFrame {
	f := &SnapshotFrame{
		ServerID:       id.String(),
		SessionID:      lv.SessionID,
		Seq:            lv.LastSeq,
		CollectedAt:    lv.Collected,
		ReceivedAt:     lv.LastFrame,
		Freshness:      freshNow(lv, now),
		NodeState:      lv.NodeState(now),
		Degraded:       lv.Degraded,
		DegradedReason: lv.DegradedBy,
		Connected:      lv.Connected,
	}
	if lv.Sample != nil {
		// Copy — never mutate the stored sample (concurrent readers).
		node := lv.Sample.Node
		node.Degraded = lv.Degraded
		node.DegradedReason = lv.DegradedBy
		f.Sample = &agentproto.Sample{Node: node}
		if s := lv.Sample; s != nil {
			f.Sample.Sites = s.Sites
		}
		f.Ring = lv.Ring
		f.Containers = lv.Containers
		f.Apps = lv.Apps
		for _, site := range lv.Sites {
			f.Sites = append(f.Sites, site)
		}
	}
	return f
}

func (s *LiveStore) snapshotFrame(serverID uuid.UUID, lv *ServerLive, now time.Time) map[string]any {
	snap := snapshotOf(serverID, lv, now)
	b, err := jsonMarshal(snap)
	if err != nil {
		return nil
	}
	var m map[string]any
	if err := jsonUnmarshal(b, &m); err != nil {
		return nil
	}
	return m
}

func freshNow(lv *ServerLive, now time.Time) map[string]any {
	ageMs := int64(-1)
	have := lv.Sample != nil
	state := agentproto.Freshness(now.Sub(lv.Collected), have)
	if have {
		ageMs = now.Sub(lv.Collected).Milliseconds()
	}
	return map[string]any{"state": state, "age_ms": ageMs}
}
