// Package dynres implements the dynamic resource ENGINE: a deterministic,
// I/O-free state machine that turns per-site pressure observations into tier
// decisions. All clocks are injected (every method takes `now`) so the
// package is fully simulatable: feed it a synthetic timeline and the exact
// sequence of tier transitions is reproducible in tests.
//
// Separation of concerns (deliberate):
//
//   - SECURITY (traffic classification, busy/attack states) lives in
//     internal/traffic + the API tick — it never runs here.
//   - RESOURCE scaling lives here: pressure = max(cpu, mem, pids, fpm)
//     against the CURRENT ceiling, smoothed with an EWMA, with asymmetric
//     hysteresis and cooldowns.
//
// Fast observation does not mean fast uncontrolled allocation: metrics may
// arrive every 2s, but decisions are emitted at the caller's cadence
// (15s) and actual resizes are cooldown-gated (60s up / 5min down).
package dynres

import (
	"fmt"
	"time"
)

// Decision thresholds and smoothing constants. These are the tuning knobs
// the 24h simulation test (simulation_test.go) validates; panel-level
// settings can override only the cooldowns/max-tier, not these — changing
// them requires re-running the simulation.
const (
	// ewmaAlpha: weight of the current sample. 0.25 ≈ a spike matters but a
	// single 2s outlier cannot flip a decision.
	ewmaAlpha = 0.25

	// scaleUpThreshold: sustained pressure at/above this (for
	// sustainedTicksUp consecutive decisions) demands more capacity.
	scaleUpThreshold = 0.80
	// scaleUpRisingThreshold: at/above this AND rising fast → scale before
	// saturation (the prediction window).
	scaleUpRisingThreshold = 0.70
	// risingMinRise: total smoothed rise over the rising window that counts
	// as "rapidly increasing".
	risingMinRise = 0.10

	// scaleDownThreshold + sustainedTicksDown: much harder to trigger than
	// scale-up (the hysteresis that prevents tier thrashing). 16 × 15s = 4min
	// of pressure below 25%.
	scaleDownThreshold = 0.25
	sustainedTicksDown = 16

	// sustainedTicksUp: 2 × 15s = 30s of sustained saturation.
	sustainedTicksUp = 2

	// maxHistory is the rolling window of smoothed pressures kept for the
	// rising detection.
	maxHistory = 4
)

// Pressures is the per-dimension usage/ceiling snapshot. Each value is
// clamped to [0,1]; a dimension with no usable limit (0 = unlimited or not
// measurable) contributes 0 and is ignored by Max.
type Pressures struct {
	CPU     float64 `json:"cpu"`
	Memory  float64 `json:"memory"`
	Pids    float64 `json:"pids"`
	FPM     float64 `json:"fpm"`
	Max     float64 `json:"max"`
	MaxName string  `json:"max_name"`
}

// Observation is one measurement of a site against its CURRENT allocation.
// Zero limits mean "unlimited / not measurable" and never produce pressure.
type Observation struct {
	CPUPercent      float64 // % of one core
	CPULimitPercent float64 // same unit; 0 = unlimited
	MemBytes        int64
	MemLimitBytes   int64 // 0 = unlimited
	Pids            int64
	PidsLimit       int64 // 0 = unlimited
	FPMActive       int   // active php-fpm workers
	FPMChildren     int   // configured pm.max_children; 0 = no pool / unknown
	FPMQueue        int   // listen queue length (>0 = workers exhausted now)
}

// Pressures reduces the observation to per-dimension pressure. The FPM
// dimension folds the listen queue in: a non-empty queue means the worker
// pool is exhausted at sampling time, so it reports near-saturation even if
// `active` dipped between samples.
func (o Observation) Pressures() Pressures {
	p := Pressures{}
	if o.CPULimitPercent > 0 {
		p.CPU = ratio(o.CPUPercent, o.CPULimitPercent)
	}
	if o.MemLimitBytes > 0 {
		p.Memory = ratio(float64(o.MemBytes), float64(o.MemLimitBytes))
	}
	if o.PidsLimit > 0 {
		p.Pids = ratio(float64(o.Pids), float64(o.PidsLimit))
	}
	if o.FPMChildren > 0 {
		fp := float64(o.FPMActive) / float64(o.FPMChildren)
		if o.FPMQueue > 0 {
			fp = max64(fp, 0.9)
		}
		p.FPM = clamp01(fp)
	}
	p.Max, p.MaxName = p.max()
	return p
}

func (p Pressures) max() (float64, string) {
	name := ""
	best := 0.0
	set := func(v float64, n string) {
		if v > best {
			best, name = v, n
		}
	}
	set(p.CPU, "cpu")
	set(p.Memory, "memory")
	set(p.Pids, "pids")
	set(p.FPM, "fpm")
	return clamp01(best), name
}

// Action is the decision outcome.
type Action string

const (
	ActionNone      Action = ""
	ActionScaleUp   Action = "scale_up"
	ActionScaleDown Action = "scale_down"
)

// Decision is what the engine wants done for one site after one decision
// tick. ActionNone means "keep the current tier".
type Decision struct {
	Action   Action
	FromTier int
	ToTier   int
	Reason   string
	Pressure float64 // smoothed pressure the decision was based on
}

// Engine is the per-site resource state machine. Create with New; all
// mutable state is here, no locks (one owner per site per tick — the API
// allocator loop). Truth for tier/state lives in the websites table; the
// engine is warm state that converges after a restart.
type Engine struct {
	Tier int

	alpha     float64
	smoothed  float64 // EWMA of Max pressure
	hist      []float64
	highStreak int // consecutive decisions with raw pressure >= scaleUpThreshold
	lowStreak  int // consecutive decisions with smoothed pressure < scaleDownThreshold
	lastUp    time.Time
	lastDown  time.Time
	// lastPressures is the latest raw reduction — exposed for status/push.
	lastPressures Pressures
}

// New returns an engine for a site at the given tier (clamped ≥1: tier 0 is
// the floor state of busy/attacked sites, and those never run this engine).
func New(tier int) *Engine {
	if tier < 1 {
		tier = 1
	}
	return &Engine{Tier: tier, alpha: ewmaAlpha}
}

// Observe ingests one sample and advances the EWMA + streaks. Call once per
// decision tick with the freshest live sample.
func (e *Engine) Observe(o Observation) Pressures {
	p := o.Pressures()
	e.lastPressures = p
	e.smoothed = e.alpha*p.Max + (1-e.alpha)*e.smoothed
	e.hist = append(e.hist, e.smoothed)
	if len(e.hist) > maxHistory {
		e.hist = e.hist[len(e.hist)-maxHistory:]
	}
	if p.Max >= scaleUpThreshold {
		e.highStreak++
	} else {
		e.highStreak = 0
	}
	if e.smoothed < scaleDownThreshold {
		e.lowStreak++
	} else {
		e.lowStreak = 0
	}
	return p
}

// Decide applies the hysteresis rules and may emit one tier decision.
//
//	Scale UP   (one tier, gated by upCooldown):
//	  - raw pressure ≥ 80% sustained (sustainedTicksUp decisions), or
//	  - smoothed ≥ 70% AND rising rapidly (prediction window)
//	Scale DOWN (one tier, gated by downCooldown):
//	  - smoothed < 25% sustained sustainedTicksDown decisions (4 min at 15s)
//
// maxTier clamps the ceiling (safety governor passes the effective cap).
func (e *Engine) Decide(now time.Time, maxTier int, upCooldown, downCooldown time.Duration) Decision {
	dec := Decision{FromTier: e.Tier, ToTier: e.Tier, Pressure: e.smoothed}
	if maxTier < 1 {
		maxTier = 1
	}

	if e.Tier < maxTier {
		sustained := e.highStreak >= sustainedTicksUp
		rising := e.smoothed >= scaleUpRisingThreshold && e.rising()
		if (sustained || rising) && e.ready(now, e.lastUp, upCooldown) {
			dec.Action = ActionScaleUp
			dec.ToTier = e.Tier + 1
			dec.Reason = e.upReason(sustained)
			e.lastUp = now
			e.highStreak = 0
			return dec
		}
	}

	if e.Tier > 1 && e.lowStreak >= sustainedTicksDown && e.ready(now, e.lastDown, downCooldown) {
		dec.Action = ActionScaleDown
		dec.ToTier = e.Tier - 1
		dec.Reason = fmt.Sprintf("pressure %.0f%% for %d decisions (hysteresis window)", e.smoothed*100, e.lowStreak)
		e.lastDown = now
		e.lowStreak = 0
	}
	return dec
}

// LastPressures exposes the most recent raw reduction (for status/push).
func (e *Engine) LastPressures() Pressures { return e.lastPressures }

// Smoothed exposes the current EWMA pressure.
func (e *Engine) Smoothed() float64 { return e.smoothed }

func (e *Engine) upReason(sustained bool) string {
	if sustained {
		return fmt.Sprintf("pressure ≥%.0f%% sustained (%s is the bottleneck)",
			scaleUpThreshold*100, e.bottleneck())
	}
	return fmt.Sprintf("pressure rising fast (%.0f%% and climbing; %s is the bottleneck)",
		e.smoothed*100, e.bottleneck())
}

func (e *Engine) bottleneck() string {
	if e.lastPressures.MaxName == "" {
		return "memory"
	}
	return e.lastPressures.MaxName
}

// rising: the smoothed pressure climbed monotonically over the history
// window with a total rise of at least risingMinRise.
func (e *Engine) rising() bool {
	if len(e.hist) < maxHistory {
		return false
	}
	h := e.hist
	for i := len(h) - 3; i < len(h)-1; i++ {
		if h[i+1] <= h[i] {
			return false
		}
	}
	return h[len(h)-1]-h[0] >= risingMinRise
}

func (e *Engine) ready(now, last time.Time, cooldown time.Duration) bool {
	if last.IsZero() {
		return true
	}
	return now.Sub(last) >= cooldown
}

func ratio(used, limit float64) float64 {
	if limit <= 0 {
		return 0
	}
	return clamp01(used / limit)
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func max64(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
