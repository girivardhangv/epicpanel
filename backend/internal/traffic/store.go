// Package traffic is the control-plane brain of the dynamic resources
// feature: it consumes per-site traffic windows streamed by the agent,
// scores them for bot/attack signals (multi-factor), and exposes the tier
// math the allocator uses to scale a site's resource allocation.
//
// Design rules:
//   - The agent reports RAW FEATURES only; every verdict is made here so
//     thresholds are tunable panel-side and every decision is auditable.
//   - The store is in-memory (like the metrics LiveStore): windows are
//     short-lived observations; durable state is the website row's
//     dynamic_state/dynamic_tier plus the dynamic_resource_events table.
//   - Classes are hysteresis-driven per site: an attack verdict must repeat
//     for N consecutive windows before the allocator suspends a site, and
//     recovery requires M clean windows — single noisy windows never flip
//     customer state.
package traffic

import (
	"sync"
	"time"

	"github.com/epicbyte/epicpanel/backend/internal/agentproto"
	"github.com/google/uuid"
)

// Class is the per-window verdict class.
type Class string

const (
	ClassLegit  Class = "legit"
	ClassBusy   Class = "busy"
	ClassAttack Class = "attack"
)

// Thresholds bound the classes on the weighted score.
type Thresholds struct {
	Busy   float64
	Attack float64
}

// DefaultThresholds: below 3 = legit; 3..6 = busy (protective throttling);
// >= 6 = attack (contributes to suspension streaks).
func DefaultThresholds() Thresholds { return Thresholds{Busy: 3, Attack: 6} }

// Factor is one scored signal in a verdict.
type Factor struct {
	Name   string  `json:"name"`
	Weight float64 `json:"weight"`
	Detail string  `json:"detail,omitempty"`
}

// Verdict is the analyzer's judgment of one window.
type Verdict struct {
	Class   Class    `json:"class"`
	Score   float64  `json:"score"`
	Factors []Factor `json:"factors,omitempty"`
}

// Trend is the running per-site decision state (updated on every window).
type Trend struct {
	Baseline     float64    `json:"baseline"`       // EWMA of requests per window
	AttackStreak int        `json:"attack_streak"`  // consecutive attack windows
	BusyStreak   int        `json:"busy_streak"`    // consecutive busy windows
	CleanStreak  int        `json:"clean_streak"`   // consecutive legit windows
	LastWindowAt time.Time  `json:"last_window_at"` // zero when never seen
	LastVerdict  Verdict    `json:"last_verdict"`
	LastWindow   int64      `json:"last_requests"` // requests in the last window
}

// Store keeps recent windows + trends per site. Concurrency-safe; the
// allocator and the ingest path are the only writers.
type Store struct {
	mu         sync.Mutex
	windows    map[uuid.UUID][]agentproto.SiteTraffic
	trends     map[uuid.UUID]*Trend
	maxWindows int
	th         Thresholds
	minRate    int64 // requests below this never trip the flood factor
	nowFn      func() time.Time
}

// NewStore wires the in-memory traffic store.
func NewStore() *Store {
	return &Store{
		windows:    map[uuid.UUID][]agentproto.SiteTraffic{},
		trends:     map[uuid.UUID]*Trend{},
		maxWindows: 20,
		th:         DefaultThresholds(),
		minRate:    30,
		nowFn:      time.Now,
	}
}

// Ingest folds a batch of completed windows (from the metrics stream) into
// trends. Returns the touched website IDs.
func (s *Store) Ingest(frames []agentproto.SiteTraffic) []uuid.UUID {
	touched := []uuid.UUID{}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range frames {
		id, err := uuid.Parse(f.WebsiteID)
		if err != nil {
			continue
		}
		w := s.windows[id]
		w = append(w, f)
		if len(w) > s.maxWindows {
			w = w[len(w)-s.maxWindows:]
		}
		s.windows[id] = w
		s.trends[id] = s.updateTrend(s.trends[id], f)
		touched = append(touched, id)
	}
	return touched
}

// updateTrend advances the EWMA baseline and the class streaks. A legit
// window resets the attack/busy streaks (consecutive semantics — flapping
// single windows must not suspend customer sites).
func (s *Store) updateTrend(t *Trend, f agentproto.SiteTraffic) *Trend {
	if t == nil {
		t = &Trend{}
	}
	verdict := AnalyzeWindow(f, t.Baseline, s.th, s.minRate)
	// Baseline: EWMA weighted to history; seeds from the first window.
	if t.LastWindowAt.IsZero() {
		t.Baseline = float64(f.Requests)
	} else {
		t.Baseline = 0.7*t.Baseline + 0.3*float64(f.Requests)
	}
	switch verdict.Class {
	case ClassAttack:
		t.AttackStreak++
		t.BusyStreak = 0
		t.CleanStreak = 0
	case ClassBusy:
		t.BusyStreak++
		t.AttackStreak = 0
		t.CleanStreak = 0
	default:
		t.AttackStreak = 0
		t.BusyStreak = 0
		t.CleanStreak++
	}
	t.LastWindowAt = s.nowFn()
	t.LastVerdict = verdict
	t.LastWindow = f.Requests
	return t
}

// Recent returns a copy of the site's recent windows (oldest first).
func (s *Store) Recent(id uuid.UUID) []agentproto.SiteTraffic {
	s.mu.Lock()
	defer s.mu.Unlock()
	w := s.windows[id]
	out := make([]agentproto.SiteTraffic, len(w))
	copy(out, w)
	return out
}

// Trend returns the running decision state for a site (ok=false when no
// window was ever ingested).
func (s *Store) Trend(id uuid.UUID) (Trend, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.trends[id]
	if !ok {
		return Trend{}, false
	}
	return *t, true
}

// Forget drops all state for a site (feature disabled / site deleted).
func (s *Store) Forget(id uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.windows, id)
	delete(s.trends, id)
}
