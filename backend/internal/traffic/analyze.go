package traffic

import (
	"fmt"

	"github.com/epicbyte/epicpanel/backend/internal/agentproto"
)

// ============================================================================
// Multi-factor window scoring.
//
// Two rules make this safe for real production sites:
//
//  1. EVIDENCE FLOORS — no factor scores from trivia. Every factor carries
//     an absolute minimum request count below which its ratio is noise
//     (one person browsing from one IP is "100% concentrated" but is a
//     person, not an attack; two 404s in a window is a typo, not a scan).
//
//  2. CLASSIFICATION FLOOR — a window below MinRequests (default 20/min)
//     is always legit regardless of score. There is nothing worth
//     defending at that volume, and false-protecting a quiet site costs
//     more user trust than missing an attacker for one window.
//
// Concentration is CORROBORATING evidence (weight 2.5): it cannot reach the
// busy class alone — it needs hostile tooling, a flood, or scanning beside
// it. Verified search/social crawlers dampen the score — real crawler
// traffic is legitimate and must scale a site up, never take it down.
// ============================================================================

// share returns v/total guarded against zero.
func share(v, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return float64(v) / float64(total)
}

// Absolute evidence floors per factor: a ratio over a handful of requests
// is anecdote, not evidence. Tuned against the simulation + live-panel
// false-positive review (single-human browsing must never classify busy).
const (
	minReqsConcentration = 30 // few IPs carrying everything matters only at volume
	minReqsTooling       = 10 // ≥10 known-scanner requests
	minReqsEmptyUA       = 10 // ≥10 requests without any UA
	minReqsHeadless      = 10 // ≥10 headless-browser requests
	minReqsScanning      = 15 // ≥15 hits on missing pages
	// Moderate flood tiers (3-10x baseline) need this volume; 10x+ never does.
	minReqsFloodModerate = 60
)

// AnalyzeWindow scores one completed window against the running baseline.
// baseline is the EWMA of recent request counts (0 = first observation —
// flood detection needs history, other factors still apply).
func AnalyzeWindow(w agentproto.SiteTraffic, baseline float64, th Thresholds, minRate int64) Verdict {
	v := Verdict{}
	if w.Requests <= 0 {
		v.Class = ClassLegit
		return v
	}

	// --- F1 volumetric flood: rate far above the site's own baseline ---
	// Moderate surges (3-10x) only count as evidence at real volume (≥60
	// reqs/window): a human's first visit on a quiet site bursts 30-50
	// requests (page + assets) against a cold baseline, which is 6x "flood"
	// but is a person. A 10x surge is never a person and classifies alone.
	if baseline > 0 && w.Requests >= minRate {
		ratio := float64(w.Requests) / baseline
		switch {
		case ratio >= 10:
			v.add("flood", 4, fmt.Sprintf("%.1fx baseline (%d vs %.0f reqs/window)", ratio, w.Requests, baseline))
		case ratio >= 5 && w.Requests >= minReqsFloodModerate:
			v.add("flood", 2.5, fmt.Sprintf("%.1fx baseline (%d vs %.0f reqs/window)", ratio, w.Requests, baseline))
		case ratio >= 3 && w.Requests >= minReqsFloodModerate:
			v.add("flood", 1, fmt.Sprintf("%.1fx baseline (%d vs %.0f reqs/window)", ratio, w.Requests, baseline))
		}
	}
	if w.TruncatedIPs > 0 && w.Requests >= 2000 {
		// Per-IP accounting saturated: more distinct clients than the cap.
		v.add("ip_saturation", 1.5, fmt.Sprintf("%d requests beyond the IP cap", w.TruncatedIPs))
	}

	// --- F2 concentration: few IPs carrying nearly all requests, at volume ---
	// Corroborating only (2.5 < busy threshold 3): a single NAT'd client or
	// one person browsing their site matches this shape at any volume, so it
	// needs hostile company to mean anything.
	if w.Requests >= minReqsConcentration {
		if w.UniqueIPs <= 3 && w.Top3Share >= 0.95 {
			v.add("concentration", 2.5, fmt.Sprintf("top-3 IPs carry %.0f%% of %d requests", w.Top3Share*100, w.Requests))
		} else if w.UniqueIPs <= 10 && w.Top3Share >= 0.8 {
			v.add("concentration", 1.5, fmt.Sprintf("top-3 IPs carry %.0f%% of %d requests", w.Top3Share*100, w.Requests))
		}
	}

	// --- F3 hostile tooling / non-browser clients (ratio AND absolute floor) ---
	if badTool := share(w.UABadTool, w.Requests); w.UABadTool >= minReqsTooling {
		switch {
		case badTool >= 0.4:
			v.add("tooling", 3, fmt.Sprintf("%.0f%% known scanner/library user-agents (%d reqs)", badTool*100, w.UABadTool))
		case badTool >= 0.15:
			v.add("tooling", 1, fmt.Sprintf("%.0f%% known scanner/library user-agents (%d reqs)", badTool*100, w.UABadTool))
		}
	}
	if emptyUA := share(w.UAEmpty, w.Requests); w.UAEmpty >= minReqsEmptyUA {
		switch {
		case emptyUA >= 0.5:
			v.add("empty_ua", 2, fmt.Sprintf("%.0f%% requests without a user-agent (%d reqs)", emptyUA*100, w.UAEmpty))
		case emptyUA >= 0.2:
			v.add("empty_ua", 0.5, fmt.Sprintf("%.0f%% requests without a user-agent (%d reqs)", emptyUA*100, w.UAEmpty))
		}
	}
	if h := share(w.UAHeadless, w.Requests); w.UAHeadless >= minReqsHeadless && h >= 0.5 {
		v.add("headless", 1, fmt.Sprintf("%.0f%% headless-browser user-agents (%d reqs)", h*100, w.UAHeadless))
	}

	// --- F4 scanning behavior: 404 storms and path churn ---
	if w.NotFoundReqs >= minReqsScanning {
		if nf := share(w.NotFoundReqs, w.Requests); nf >= 0.6 {
			v.add("scanning", 3, fmt.Sprintf("%.0f%% of requests hit missing pages (%d)", nf*100, w.NotFoundReqs))
		} else if nf >= 0.35 {
			v.add("scanning", 1.5, fmt.Sprintf("%.0f%% of requests hit missing pages (%d)", nf*100, w.NotFoundReqs))
		}
	}
	if w.PathSamples >= 100 && share(int64(w.UniquePaths), int64(w.PathSamples)) >= 0.9 {
		v.add("path_churn", 1, fmt.Sprintf("%d distinct paths, almost no repeats", w.UniquePaths))
	}

	// --- F5 method abuse: POST floods (brute force / form spam) ---
	if p := share(w.PostReqs, w.Requests); p >= 0.6 && w.Requests >= 100 {
		v.add("post_flood", 1, fmt.Sprintf("%.0f%% POST requests (%d)", p*100, w.PostReqs))
	}

	// --- Dampener: verified crawler traffic is legitimate ---
	if g := share(w.UAGoodBot, w.Requests); g >= 0.5 {
		v.add("verified_crawler", -2, fmt.Sprintf("%.0f%% verified crawler traffic", g*100))
	}

	// --- Classification floor: quiet windows are never hostile ---
	if w.Requests < th.MinRequests {
		v.Class = ClassLegit
		return v
	}
	if v.Score >= th.Attack {
		v.Class = ClassAttack
	} else if v.Score >= th.Busy {
		v.Class = ClassBusy
	} else {
		v.Class = ClassLegit
	}
	return v
}

func (v *Verdict) add(name string, weight float64, detail string) {
	v.Score += weight
	v.Factors = append(v.Factors, Factor{Name: name, Weight: weight, Detail: detail})
	if v.Score < 0 {
		v.Score = 0
	}
}
