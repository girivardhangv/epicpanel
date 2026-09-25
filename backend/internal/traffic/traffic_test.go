package traffic

import (
	"testing"
	"time"

	"github.com/epicbyte/epicpanel/backend/internal/agentproto"
	"github.com/google/uuid"
)

const testSite = "11111111-1111-1111-1111-111111111111"

func win(mutators ...func(*agentproto.SiteTraffic)) agentproto.SiteTraffic {
	w := agentproto.SiteTraffic{WebsiteID: testSite, WindowS: 60, Requests: 100}
	for _, m := range mutators {
		m(&w)
	}
	return w
}

func TestAnalyzeWindowCleanTraffic(t *testing.T) {
	v := AnalyzeWindow(win(), 100, DefaultThresholds(), 30)
	if v.Class != ClassLegit || v.Score != 0 {
		t.Fatalf("clean traffic scored %+v", v)
	}
}

func TestAnalyzeWindowMultiFactorAttack(t *testing.T) {
	// A flood from a few IPs with scanner tooling and a 404 storm — each
	// factor contributes; the sum must cross the attack threshold.
	w := win(
		func(x *agentproto.SiteTraffic) { x.Requests = 1500 }, // 15x baseline
		func(x *agentproto.SiteTraffic) { x.UniqueIPs = 2; x.Top3Share = 0.98 },
		func(x *agentproto.SiteTraffic) { x.NotFoundReqs = 1200 },
		func(x *agentproto.SiteTraffic) { x.UABadTool = 900 },
	)
	v := AnalyzeWindow(w, 100, DefaultThresholds(), 30)
	if v.Class != ClassAttack {
		t.Fatalf("multi-factor attack scored %.1f (%s), want attack", v.Score, v.Class)
	}
	if len(v.Factors) < 3 {
		t.Fatalf("expected multiple contributing factors, got %+v", v.Factors)
	}
}

func TestAnalyzeWindowSmallSiteNoFloodFalsePositive(t *testing.T) {
	// A tiny site (baseline ~5) suddenly getting 20 requests must NOT trip
	// the flood factor (below minRate); other factors still apply.
	w := win(func(x *agentproto.SiteTraffic) { x.Requests = 20 })
	v := AnalyzeWindow(w, 5, DefaultThresholds(), 30)
	if v.Class != ClassLegit {
		t.Fatalf("small burst scored %+v, want legit", v)
	}
}

func TestAnalyzeWindowVerifiedCrawlerDampens(t *testing.T) {
	// Googlebot flood: volumetric but verified crawler — must not classify
	// as attack.
	w := win(
		func(x *agentproto.SiteTraffic) { x.Requests = 1200 },
		func(x *agentproto.SiteTraffic) { x.UAGoodBot = 1100 },
	)
	v := AnalyzeWindow(w, 100, DefaultThresholds(), 30)
	if v.Class == ClassAttack {
		t.Fatalf("crawler flood scored %.1f attack, want dampened", v.Score)
	}
}

func TestAnalyzeWindowScannerOnly(t *testing.T) {
	// Low volume but pure 404 storm from scanner UAs — busy class.
	w := win(
		func(x *agentproto.SiteTraffic) { x.Requests = 60 },
		func(x *agentproto.SiteTraffic) { x.NotFoundReqs = 50 },
		func(x *agentproto.SiteTraffic) { x.UABadTool = 45 },
	)
	v := AnalyzeWindow(w, 10, DefaultThresholds(), 30)
	if v.Class == ClassLegit {
		t.Fatalf("scanner traffic passed as legit: %.1f", v.Score)
	}
}

func TestStoreConsecutiveStreaks(t *testing.T) {
	s := NewStore()
	id := uuid.MustParse(testSite)
	attack := win(func(x *agentproto.SiteTraffic) {
		x.Requests = 2000
		x.UniqueIPs = 1
		x.Top3Share = 1
		x.UABadTool = 1500
		x.NotFoundReqs = 1800
	})
	clean := win()

	s.Ingest([]agentproto.SiteTraffic{attack, clean, attack})
	trend, ok := s.Trend(id)
	if !ok {
		t.Fatal("no trend")
	}
	// clean window must reset the attack streak (consecutive semantics).
	if trend.AttackStreak != 1 || trend.CleanStreak != 0 {
		t.Fatalf("streaks after attack/clean/attack: %+v", trend)
	}
	if trend.LastVerdict.Class != ClassAttack {
		t.Fatalf("last verdict %+v", trend.LastVerdict)
	}

	s.Ingest([]agentproto.SiteTraffic{clean, clean})
	trend, _ = s.Trend(id)
	if trend.AttackStreak != 0 || trend.CleanStreak != 2 {
		t.Fatalf("streaks after two clean windows: %+v", trend)
	}
}

func TestStoreBaselineEWMA(t *testing.T) {
	s := NewStore()
	id := uuid.MustParse(testSite)
	s.Ingest([]agentproto.SiteTraffic{win(), win(func(x *agentproto.SiteTraffic) { x.Requests = 200 })})
	trend, _ := s.Trend(id)
	// seed = 100, then 0.7*100 + 0.3*200 = 130
	if trend.Baseline < 129 || trend.Baseline > 131 {
		t.Fatalf("baseline = %.1f, want ~130", trend.Baseline)
	}
}

func TestStoreRecentAndForget(t *testing.T) {
	s := NewStore()
	id := uuid.MustParse(testSite)
	s.Ingest([]agentproto.SiteTraffic{win(), win()})
	if got := len(s.Recent(id)); got != 2 {
		t.Fatalf("recent = %d, want 2", got)
	}
	s.Forget(id)
	if got := len(s.Recent(id)); got != 0 {
		t.Fatalf("after forget recent = %d", got)
	}
	if _, ok := s.Trend(id); ok {
		t.Fatal("trend survived Forget")
	}
}

func TestStoreWindowCap(t *testing.T) {
	s := NewStore()
	id := uuid.MustParse(testSite)
	frames := []agentproto.SiteTraffic{}
	for i := 0; i < 30; i++ {
		frames = append(frames, win())
	}
	s.Ingest(frames)
	if got := len(s.Recent(id)); got != 20 {
		t.Fatalf("window ring = %d, want capped 20", got)
	}
}

func TestScaleTiers(t *testing.T) {
	base := BaseLimits{MemoryMB: 512, CPUPercent: 100, PidsMax: 64, FpmChildren: 16}

	// Floor: 32MB, 20% CPU (min 10), quarter pids, 1 fpm child.
	f := Scale(base, 0, 32)
	if f.MemoryMB != 32 || f.CPUPercent != 20 || f.PidsMax != 16 || f.FpmChildren != 1 {
		t.Fatalf("floor = %+v", f)
	}

	// Tier 1 = base exactly.
	b := Scale(base, 1, 32)
	if b.MemoryMB != 512 || b.CPUPercent != 100 || b.PidsMax != 64 || b.FpmChildren != 16 {
		t.Fatalf("base tier = %+v", b)
	}

	// Tier 4 = 8x with caps.
	m := Scale(base, 4, 32)
	if m.MemoryMB != 4096 || m.CPUPercent != 800 || m.PidsMax != 512 {
		t.Fatalf("8x tier = %+v", m)
	}
	if m.FpmChildren != 64 { // 4096/32 = 128, capped at base children x4 = 64
		t.Fatalf("8x fpm children = %d", m.FpmChildren)
	}

	// Out-of-range tiers clamp.
	if Scale(base, 99, 32).MemoryMB != 4096 {
		t.Fatal("tier clamp failed")
	}

	// Unlimited base stays unlimited at the floor.
	u := Scale(BaseLimits{}, 0, 32)
	if u.MemoryMB != 32 || u.CPUPercent != 0 || u.PidsMax != 0 {
		t.Fatalf("unlimited floor = %+v", u)
	}

	// No floor configured: tier 0 keeps base memory.
	nf := Scale(base, 0, 0)
	if nf.MemoryMB != 512 {
		t.Fatalf("no-floor tier0 = %+v", nf)
	}

	// Small plan: children never explode past base*4.
	small := Scale(BaseLimits{MemoryMB: 64, FpmChildren: 2}, 4, 32)
	if small.FpmChildren > 8 {
		t.Fatalf("small-plan children = %d", small.FpmChildren)
	}
}

func TestClampTierAndDescribe(t *testing.T) {
	if ClampTier(-3) != 0 || ClampTier(1) != 1 || ClampTier(99) != MaxTier {
		t.Fatal("ClampTier out of range")
	}
	if Describe(0, 32) != "floor (32MB)" || Describe(1, 32) != "base" {
		t.Fatalf("describe: %q %q", Describe(0, 32), Describe(1, 32))
	}
}

func TestTrendLastWindowAtUsesClock(t *testing.T) {
	s := NewStore()
	id := uuid.MustParse(testSite)
	before := time.Now().Add(-time.Minute)
	s.Ingest([]agentproto.SiteTraffic{win()})
	trend, _ := s.Trend(id)
	if trend.LastWindowAt.Before(before) || trend.LastWindowAt.After(time.Now().Add(time.Minute)) {
		t.Fatalf("LastWindowAt = %v, want ~now", trend.LastWindowAt)
	}
}

// ============================================================================
// False-positive regression (live-panel review 2026-09-25): wptest.example
// was floored by "concentration" because ONE person browsed their site from
// one IP — and the panel's own health checker kept the site in busy
// forever. A quiet site with a human behind it must always classify legit.
// ============================================================================

func TestAnalyzeWindowSingleHumanNeverBusy(t *testing.T) {
	// The exact live false positive: 14 reqs/min, one IP, a real Firefox.
	w := win(
		func(x *agentproto.SiteTraffic) { x.Requests = 14 },
		func(x *agentproto.SiteTraffic) { x.UniqueIPs = 1; x.Top3Share = 1.0 },
		func(x *agentproto.SiteTraffic) { x.Status2xx = 14 },
	)
	v := AnalyzeWindow(w, 4.9, DefaultThresholds(), 30)
	if v.Class != ClassLegit {
		t.Fatalf("single human browsing scored %.1f (%s), want legit: %+v", v.Score, v.Class, v.Factors)
	}
	// Even a chatty human (30 reqs from one IP) must stay legit —
	// concentration alone (2.5) cannot reach the busy threshold (3).
	w = win(
		func(x *agentproto.SiteTraffic) { x.Requests = 30 },
		func(x *agentproto.SiteTraffic) { x.UniqueIPs = 1; x.Top3Share = 1.0 },
		func(x *agentproto.SiteTraffic) { x.Status2xx = 30 },
	)
	if v = AnalyzeWindow(w, 4.9, DefaultThresholds(), 30); v.Class != ClassLegit {
		t.Fatalf("one heavy human scored %.1f (%s), want legit: %+v", v.Score, v.Class, v.Factors)
	}
	// The classification floor: an absurd score on a tiny window is still
	// trivia (2 reqs, both 404, empty UA) — no class above legit.
	w = win(
		func(x *agentproto.SiteTraffic) { x.Requests = 2 },
		func(x *agentproto.SiteTraffic) { x.Status4xx = 2; x.NotFoundReqs = 2 },
		func(x *agentproto.SiteTraffic) { x.UAEmpty = 2 },
		func(x *agentproto.SiteTraffic) { x.UniqueIPs = 1; x.Top3Share = 1.0 },
	)
	if v = AnalyzeWindow(w, 1, DefaultThresholds(), 30); v.Class != ClassLegit {
		t.Fatalf("trivia window classified %s (score %.1f) — MinRequests floor failed", v.Class, v.Score)
	}
}

func TestAnalyzeWindowConcentrationNeedsCompany(t *testing.T) {
	// 50 reqs from one IP with a normal browser: above every evidence floor,
	// but concentration (2.5) alone stays below busy (3) — it corroborates,
	// it does not classify.
	w := win(
		func(x *agentproto.SiteTraffic) { x.Requests = 50 },
		func(x *agentproto.SiteTraffic) { x.UniqueIPs = 1; x.Top3Share = 1.0 },
		func(x *agentproto.SiteTraffic) { x.Status2xx = 50 },
	)
	v := AnalyzeWindow(w, 50, DefaultThresholds(), 30)
	if v.Class != ClassLegit {
		t.Fatalf("one-IP browsing at volume scored %.1f (%s), want legit", v.Score, v.Class)
	}

	// The same shape WITH hostile tooling (scanner UAs) is real bot traffic:
	// 2.5 + 3 = 5.5 → busy, and sustained it floors protectively.
	w = win(
		func(x *agentproto.SiteTraffic) { x.Requests = 50 },
		func(x *agentproto.SiteTraffic) { x.UniqueIPs = 1; x.Top3Share = 1.0 },
		func(x *agentproto.SiteTraffic) { x.UABadTool = 30 },
	)
	if v = AnalyzeWindow(w, 50, DefaultThresholds(), 30); v.Class != ClassBusy {
		t.Fatalf("concentration + scanner tooling scored %.1f (%s), want busy", v.Score, v.Class)
	}
}

func TestAnalyzeWindowRatioFactorsNeedAbsoluteEvidence(t *testing.T) {
	// Ratios without volume are anecdote: 3 requests with 1 empty UA (33%),
	// 2 of 4 404s (50%), 3 of 10 scanner UAs — every ratio trips, nothing
	// reaches its absolute floor → legit.
	w := win(
		func(x *agentproto.SiteTraffic) { x.Requests = 10 },
		func(x *agentproto.SiteTraffic) { x.UAEmpty = 3; x.UABadTool = 3 },
		func(x *agentproto.SiteTraffic) { x.NotFoundReqs = 4 },
		func(x *agentproto.SiteTraffic) { x.Status4xx = 7 },
	)
	v := AnalyzeWindow(w, 10, DefaultThresholds(), 30)
	if v.Class != ClassLegit {
		t.Fatalf("anecdote-ratio window scored %.1f (%s), want legit: %+v", v.Score, v.Class, v.Factors)
	}
}
