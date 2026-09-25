package dynres

import (
	"testing"
	"time"
)

// ============================================================================
// 24-hour deterministic simulation.
//
// The engine is fed a synthetic workload at the real decision cadence
// (15s ticks ⇒ 5760 decisions/day) and the test asserts the EXACT sequence
// of tier transitions. This is the gate that must pass before the engine is
// allowed to touch real cgroups: every transition is explainable, no
// thrashing, no oscillation, hysteresis respected.
//
// Workload model: the site's live memory usage is `load × baseMem` (load in
// multiples of the tier-1 allocation); the engine sees pressure against the
// CURRENT tier's ceiling, so scaling up visibly drops pressure — same
// feedback loop as production.
// ============================================================================

const (
	simBaseMB = 256
	simTick   = 15 * time.Second
	simUpCD   = time.Minute
	simDownCD = 5 * time.Minute
)

var tierMult = map[int]float64{1: 1, 2: 2, 3: 4, 4: 8}

type simTransition struct {
	tick  int
	at    time.Time
	from  int
	to    int
	cause string // "sustained" or "rising" or "hysteresis"
}

type simResult struct {
	transitions []simTransition
	finalTier   int
	ticks       int
}

// runSim steps the engine through ticksPerPhase-based phases. Each phase is
// (durationTicks, load); load is in multiples of the tier-1 base allocation.
func runSim(phases []struct {
	ticks int
	load  float64
}, maxTier int) simResult {
	e := New(1)
	start := time.Unix(0, 0)
	res := simResult{}
	tick := 0
	for _, ph := range phases {
		for i := 0; i < ph.ticks; i++ {
			now := start.Add(time.Duration(tick+1) * simTick)
			o := Observation{MemBytes: int64(ph.load * simBaseMB * 1024 * 1024)}
			o.MemLimitBytes = int64(tierMult[e.Tier] * simBaseMB * 1024 * 1024)
			e.Observe(o)
			d := e.Decide(now, maxTier, simUpCD, simDownCD)
			if d.Action == ActionScaleUp || d.Action == ActionScaleDown {
				res.transitions = append(res.transitions, simTransition{
					tick: tick + 1, at: now, from: d.FromTier, to: d.ToTier,
					cause: map[Action]string{ActionScaleUp: upCause(e), ActionScaleDown: "hysteresis"}[d.Action],
				})
				e.Tier = d.ToTier
			}
			tick++
		}
	}
	res.finalTier = e.Tier
	res.ticks = tick
	return res
}

func upCause(e *Engine) string {
	if e.highStreak == 0 && e.smoothed >= scaleUpRisingThreshold {
		return "rising"
	}
	return "sustained"
}

func step(t *testing.T, res simResult, i int) simTransition {
	t.Helper()
	if i >= len(res.transitions) {
		t.Fatalf("expected more transitions; got %d: %+v", len(res.transitions), res.transitions)
	}
	return res.transitions[i]
}

func TestSimulation24Hours(t *testing.T) {
	const h = 240 // ticks per hour

	// Day profile:
	//   00-02h  quiet night (load 0.10 of base)
	//   02h     morning ramp (load 0.90 — sustained saturation)
	//   03h     growth continues (load 1.80)
	//   04-06h  peak (load 3.70)
	//   06h     traffic falls off a cliff (load 0.05) — idle evening
	//   then quiet for the rest of the 24h.
	phases := []struct {
		ticks int
		load  float64
	}{
		{2 * h, 0.10},
		{1 * h, 0.90},
		{1 * h, 1.80},
		{2 * h, 3.70},
		{18 * h, 0.05},
	}

	res := runSim(phases, 4)
	if res.ticks != 24*h {
		t.Fatalf("simulation must cover 24h: %d ticks", res.ticks)
	}

	// --- scale-ups: exact ticks, exact tiers ---
	// Peak at 02h: two consecutive ≥80% ticks ⇒ scale at tick 2h+30s.
	if s := step(t, res, 0); s.tick != 2*h+2 || s.from != 1 || s.to != 2 || s.cause != "sustained" {
		t.Fatalf("transition 0: expected 1→2 sustained at tick %d, got %+v", 2*h+2, s)
	}
	// 03h step to 1.8×base: pressure 0.90 against tier-2 ceiling ⇒ scale at 3h+30s.
	if s := step(t, res, 1); s.tick != 3*h+2 || s.from != 2 || s.to != 3 {
		t.Fatalf("transition 1: expected 2→3 at tick %d, got %+v", 3*h+2, s)
	}
	// 04h step to 3.7×base: pressure 0.925 against tier-3 ceiling ⇒ scale at 4h+30s.
	if s := step(t, res, 2); s.tick != 4*h+2 || s.from != 3 || s.to != 4 {
		t.Fatalf("transition 2: expected 3→4 at tick %d, got %+v", 4*h+2, s)
	}

	// --- scale-downs: hysteresis-gated, spaced by the 5min cooldown ---
	// Cliff at 06h. First down: smoothed must decay below 25% AND hold 16
	// ticks ⇒ strictly after 06h+4min, well after the 60s up-cooldown.
	d1 := step(t, res, 3)
	if d1.from != 4 || d1.to != 3 || d1.tick <= 6*h+16 {
		t.Fatalf("first down: expected 4→3 after 06h+16 ticks, got %+v", d1)
	}
	d2 := step(t, res, 4)
	if d2.from != 3 || d2.to != 2 {
		t.Fatalf("second down: expected 3→2, got %+v", d2)
	}
	d3 := step(t, res, 5)
	if d3.from != 2 || d3.to != 1 {
		t.Fatalf("third down: expected 2→1, got %+v", d3)
	}
	gap := func(a, b simTransition) int { return b.tick - a.tick }
	if minGap := int(simDownCD / simTick); gap(d1, d2) < minGap || gap(d2, d3) < minGap {
		t.Fatalf("scale-downs must respect the 5min cooldown: %d, %d", gap(d1, d2), gap(d2, d3))
	}

	// --- stability: after settling, the remaining day must be SILENT ---
	if n := len(res.transitions); n != 6 {
		t.Fatalf("a 24h day must produce exactly 6 transitions (no thrashing), got %d: %+v",
			n, res.transitions)
	}
	if res.finalTier != 1 {
		t.Fatalf("quiet night must settle at tier 1, got %d", res.finalTier)
	}
}

// TestSimulationGovernorClamp simulates the full pipeline WITH the governor
// on the same heavy workload (3.7×base, which alone would drive the engine
// to tier 4 = 8×base = 2GB). On a roomy node it must climb freely; on a
// 1GB node the reserve check must pin it at tier 2 (512MB is all that fits
// under the 10% reserve) without destabilizing.
func TestSimulationGovernorClamp(t *testing.T) {
	const MB = 1024 * 1024
	const h = 240
	run := func(nodeTotal int64) (tier int, denials int, reasons []string) {
		e := New(1)
		start := time.Unix(0, 0)
		tier = 1
		for tick := 0; tick < 8*h; tick++ {
			now := start.Add(time.Duration(tick+1) * simTick)
			load := 3.7 // ×base, exceeds the tier-4 ceiling — forces tier attempts
			o := Observation{MemBytes: int64(load * simBaseMB * MB)}
			o.MemLimitBytes = int64(tierMult[tier] * simBaseMB * MB)
			e.Observe(o)
			d := e.Decide(now, 4, simUpCD, simDownCD)
			if d.Action == ActionScaleUp {
				current := int64(tierMult[tier] * simBaseMB * MB)
				desired := int64(tierMult[d.ToTier] * simBaseMB * MB)
				v := Govern(GovernorInput{
					Tier: tier, DesiredTier: d.ToTier,
					CurrentMemBytes: current, DesiredMemBytes: desired,
					NodeMemTotalBytes:     nodeTotal,
					NodeMemAvailableBytes: nodeTotal - current - 100*MB, // 100MB used by others
					MaxTier:               4,
				})
				if !v.Allowed {
					denials++
					reasons = append(reasons, v.Reason)
					if v.EffectiveTier != tier {
						t.Fatalf("denial must not change the tier: %+v", v)
					}
				} else {
					tier = v.EffectiveTier
					e.Tier = tier
				}
			}
		}
		return tier, denials, reasons
	}

	// 8GB node: 2048MB + 100MB + reserve fits easily → tier 4, zero denials.
	tier, denials, _ := run(8 * 1024 * MB)
	if tier != 4 || denials != 0 {
		t.Fatalf("roomy node must reach tier 4 unimpeded: tier=%d denials=%d", tier, denials)
	}

	// 1GB node: tier 2 (512MB) fits; tier 3 (1024MB) violates the reserve.
	tier, denials, reasons := run(1024 * MB)
	if tier != 2 {
		t.Fatalf("1GB node must cap at tier 2, settled at %d (denials: %v)", tier, reasons)
	}
	if denials == 0 {
		t.Fatal("governor must have denied at least one over-capacity scale-up")
	}
}
