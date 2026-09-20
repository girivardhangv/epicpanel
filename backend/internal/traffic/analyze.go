package traffic

import (
	"fmt"

	"github.com/epicbyte/epicpanel/backend/internal/agentproto"
)

// ============================================================================
// Multi-factor window scoring.
//
// No single signal decides: a flood alone, a scanner's 404 storm, a
// concentrated top-talker and hostile tooling each contribute weight, and
// only the SUM crosses the class thresholds. Verified search/social crawlers
// dampen the score — real crawler traffic is legitimate and must scale a
// site up, never take it down.
// ============================================================================

// share returns v/total guarded against zero.
func share(v, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return float64(v) / float64(total)
}

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
	if baseline > 0 && w.Requests >= minRate {
		ratio := float64(w.Requests) / baseline
		switch {
		case ratio >= 10:
			v.add("flood", 4, fmt.Sprintf("%.1fx baseline (%d vs %.0f reqs/window)", ratio, w.Requests, baseline))
		case ratio >= 5:
			v.add("flood", 2.5, fmt.Sprintf("%.1fx baseline (%d vs %.0f reqs/window)", ratio, w.Requests, baseline))
		case ratio >= 3:
			v.add("flood", 1, fmt.Sprintf("%.1fx baseline (%d vs %.0f reqs/window)", ratio, w.Requests, baseline))
		}
	}
	if w.TruncatedIPs > 0 && w.Requests >= 2000 {
		// Per-IP accounting saturated: more distinct clients than the cap.
		v.add("ip_saturation", 1.5, fmt.Sprintf("%d requests beyond the IP cap", w.TruncatedIPs))
	}

	// --- F2 concentration: few IPs carrying nearly all requests ---
	if w.UniqueIPs <= 3 && w.Top3Share >= 0.95 {
		v.add("concentration", 3, fmt.Sprintf("top-3 IPs carry %.0f%% of %d requests", w.Top3Share*100, w.Requests))
	} else if w.UniqueIPs <= 10 && w.Top3Share >= 0.8 {
		v.add("concentration", 1.5, fmt.Sprintf("top-3 IPs carry %.0f%% of %d requests", w.Top3Share*100, w.Requests))
	}

	// --- F3 hostile tooling / non-browser clients ---
	badTool := share(w.UABadTool, w.Requests)
	switch {
	case badTool >= 0.4:
		v.add("tooling", 3, fmt.Sprintf("%.0f%% known scanner/library user-agents", badTool*100))
	case badTool >= 0.15:
		v.add("tooling", 1, fmt.Sprintf("%.0f%% known scanner/library user-agents", badTool*100))
	}
	emptyUA := share(w.UAEmpty, w.Requests)
	switch {
	case emptyUA >= 0.5:
		v.add("empty_ua", 2, fmt.Sprintf("%.0f%% requests without a user-agent", emptyUA*100))
	case emptyUA >= 0.2:
		v.add("empty_ua", 0.5, fmt.Sprintf("%.0f%% requests without a user-agent", emptyUA*100))
	}
	if h := share(w.UAHeadless, w.Requests); h >= 0.5 {
		v.add("headless", 1, fmt.Sprintf("%.0f%% headless-browser user-agents", h*100))
	}

	// --- F4 scanning behavior: 404 storms and path churn ---
	nf := share(w.NotFoundReqs, w.Requests)
	switch {
	case nf >= 0.6:
		v.add("scanning", 3, fmt.Sprintf("%.0f%% of requests hit missing pages", nf*100))
	case nf >= 0.35:
		v.add("scanning", 1.5, fmt.Sprintf("%.0f%% of requests hit missing pages", nf*100))
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
