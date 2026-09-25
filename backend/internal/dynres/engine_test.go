package dynres

import (
	"testing"
	"time"
)

var obs = func(memPressure float64) Observation {
	return Observation{MemBytes: int64(memPressure * 1000), MemLimitBytes: 1000}
}

func TestPressuresBasics(t *testing.T) {
	// Unlimited dimensions contribute nothing.
	p := Observation{CPUPercent: 90, MemBytes: 500, MemLimitBytes: 1000}.Pressures()
	if p.CPU != 0 || p.Memory != 0.5 || p.MaxName != "memory" {
		t.Fatalf("got %+v", p)
	}
	// max() picks the bottleneck, not the average (RAM 95% + CPU 30% → RAM).
	p = Observation{CPUPercent: 30, CPULimitPercent: 100, MemBytes: 950, MemLimitBytes: 1000}.Pressures()
	if p.Max != 0.95 || p.MaxName != "memory" {
		t.Fatalf("max pressure must be the bottleneck: %+v", p)
	}
	// A non-empty FPM listen queue means workers are exhausted right now.
	p = Observation{FPMActive: 2, FPMChildren: 10, FPMQueue: 3}.Pressures()
	if p.FPM < 0.9 {
		t.Fatalf("listen queue must read as near-saturation: %+v", p)
	}
	// Clamped at 1.
	p = Observation{MemBytes: 2000, MemLimitBytes: 1000}.Pressures()
	if p.Memory != 1 {
		t.Fatalf("pressure must clamp: %+v", p)
	}
}

func TestEWMASmoothing(t *testing.T) {
	e := New(1)
	// A single 2s outlier must not move the decision state much.
	e.Observe(obs(1.0))
	if e.Smoothed() > 0.26 {
		t.Fatalf("one spike dominated the EWMA: %.3f", e.Smoothed())
	}
	// Convergence to a steady value.
	for i := 0; i < 40; i++ {
		e.Observe(obs(0.5))
	}
	if e.Smoothed() < 0.49 || e.Smoothed() > 0.51 {
		t.Fatalf("EWMA did not converge to 0.5: %.3f", e.Smoothed())
	}
}

func TestSustainedScaleUpAndCooldown(t *testing.T) {
	e := New(1)
	t0 := time.Unix(0, 0)
	e.Observe(obs(0.90))
	if d := e.Decide(t0, 4, time.Minute, 5*time.Minute); d.Action != ActionNone {
		t.Fatalf("one saturated tick must not scale: %+v", d)
	}
	e.Observe(obs(0.90))
	d := e.Decide(t0.Add(15*time.Second), 4, time.Minute, 5*time.Minute)
	if d.Action != ActionScaleUp || d.ToTier != 2 {
		t.Fatalf("two saturated ticks must scale up: %+v", d)
	}
	e.Tier = 2

	// Cooldown: still saturated, but the next tick is blocked.
	e.Observe(obs(0.90))
	e.Observe(obs(0.90))
	if d := e.Decide(t0.Add(30*time.Second), 4, time.Minute, 5*time.Minute); d.Action != ActionNone {
		t.Fatalf("cooldown must block: %+v", d)
	}
	if d := e.Decide(t0.Add(76*time.Second), 4, time.Minute, 5*time.Minute); d.Action != ActionScaleUp || d.ToTier != 3 {
		t.Fatalf("after the cooldown it must scale (lastUp was t+15s): %+v", d)
	}
}

func TestMaxTierCeiling(t *testing.T) {
	e := New(4)
	e.Observe(obs(1))
	if d := e.Decide(time.Unix(0, 0), 4, 0, 0); d.Action != ActionNone {
		t.Fatalf("tier 4 is the ceiling: %+v", d)
	}
	e = New(4)
	e.Observe(obs(1))
	e.Observe(obs(1))
	if d := e.Decide(time.Unix(0, 0), 8, 0, 0); d.Action != ActionScaleUp {
		t.Fatalf("maxTier is the passed-in ceiling: %+v", d)
	}
}

func TestRisingScaleUpBeforeSaturation(t *testing.T) {
	// Bursty load that never holds 80% for two CONSECUTIVE ticks but trends
	// upward — the prediction window must catch it before saturation.
	e := New(1)
	t0 := time.Unix(0, 0)
	bursts := []float64{0.60, 0.85, 0.65, 0.88, 0.70, 0.90, 0.72, 0.92}
	for i, p := range bursts {
		e.Observe(obs(p))
		if d := e.Decide(t0.Add(time.Duration(i+1)*15*time.Second), 4, 0, 0); d.Action == ActionScaleUp {
			if e.Smoothed() < scaleUpRisingThreshold {
				t.Fatalf("scaled below the rising gate: smoothed %.3f", e.Smoothed())
			}
			return // caught — prediction window worked
		}
	}
	t.Fatalf("rising bursty load never triggered the prediction window (smoothed %.3f)", e.Smoothed())
}

func TestScaleDownHysteresis(t *testing.T) {
	e := New(2)
	t0 := time.Unix(0, 0)
	// 15 quiet ticks: not enough.
	for i := 1; i <= 15; i++ {
		e.Observe(obs(0.05))
		if d := e.Decide(t0.Add(time.Duration(i)*15*time.Second), 4, 0, 5*time.Minute); d.Action != ActionNone {
			t.Fatalf("scaled down after only %d ticks: %+v", i, d)
		}
	}
	// The 16th (4 min) releases it.
	e.Observe(obs(0.05))
	if d := e.Decide(t0.Add(16*15*time.Second), 4, 0, 5*time.Minute); d.Action != ActionScaleDown || d.ToTier != 1 {
		t.Fatalf("4 quiet minutes must scale down one tier: %+v", d)
	}
	// Never below tier 1 while active.
	e.Tier = 1
	e.Observe(obs(0.05))
	if d := e.Decide(t0.Add(time.Hour), 4, 0, 0); d.Action != ActionNone {
		t.Fatalf("active sites floor at tier 1: %+v", d)
	}
}

func TestDownCooldownSpacing(t *testing.T) {
	e := New(3)
	t0 := time.Unix(0, 0)
	for i := 0; i < 16; i++ {
		e.Observe(obs(0.05))
	}
	if d := e.Decide(t0, 4, time.Minute, 5*time.Minute); d.Action != ActionScaleDown {
		t.Fatalf("pre-cooldown down must fire: %+v", d)
	}
	e.Tier = 2
	for i := 0; i < 30; i++ {
		e.Observe(obs(0.05))
	}
	// 5min cooldown from t0 → blocked at t+299s; fires at t+300s.
	if d := e.Decide(t0.Add(299*time.Second), 4, time.Minute, 5*time.Minute); d.Action != ActionNone {
		t.Fatalf("down cooldown must block: %+v", d)
	}
	if d := e.Decide(t0.Add(300*time.Second), 4, time.Minute, 5*time.Minute); d.Action != ActionScaleDown {
		t.Fatalf("down must fire after its cooldown: %+v", d)
	}
}

func TestGovernor(t *testing.T) {
	const MB = 1024 * 1024
	base := GovernorInput{
		Tier: 2, DesiredTier: 3,
		CurrentMemBytes: 512 * MB, DesiredMemBytes: 1024 * MB,
		NodeMemTotalBytes: 16 * 1024 * MB, MaxTier: 4,
	}
	base.NodeMemAvailableBytes = 4 * 1024 * MB

	// Happy path.
	if v := Govern(base); !v.Allowed || v.EffectiveTier != 3 {
		t.Fatalf("normal scale-up must pass: %+v", v)
	}
	// Per-site ceiling.
	c := base
	c.DesiredTier = 5
	if v := Govern(c); v.Allowed {
		t.Fatalf("above the site ceiling must be denied: %+v", v)
	}
	// Global rate limit.
	c = base
	c.ScaleUpsLastMinute, c.MaxScaleUpsPerMinute = 10, 10
	if v := Govern(c); v.Allowed {
		t.Fatalf("rate-limited scale-up must be denied: %+v", v)
	}
	// Fleet cap: 15.2GB allocated of 16GB node, +512MB over the 75% cap.
	c = base
	c.GlobalDynamicAllocatedBytes = 12 * 1024 * MB
	c.GlobalCapPercent = 75
	if v := Govern(c); v.Allowed {
		t.Fatalf("fleet cap must be denied: %+v", v)
	}
	// Node reserve: only 600MB available, need +512MB, 10% of 16GB reserved.
	c = base
	c.NodeMemAvailableBytes = 600 * MB
	if v := Govern(c); v.Allowed {
		t.Fatalf("node reserve must be denied: %+v", v)
	}
	// Scale-down is never blocked.
	c = base
	c.DesiredTier = 1
	c.NodeMemAvailableBytes = 0
	if v := Govern(c); !v.Allowed {
		t.Fatalf("scale-down must never be blocked: %+v", v)
	}
}
