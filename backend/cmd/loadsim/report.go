package main

import (
	"sort"
	"time"
)

// report is the JSON document loadsim prints (and optionally writes) at the
// end of a run. Every field is measured; extrapolations live only in
// docs/scale-report.md prose, never in this file.
type report struct {
	Label     string          `json:"label"`
	Version   string          `json:"loadsim_version"`
	API       string          `json:"api"`
	Nodes     int             `json:"nodes"`
	Sites     int             `json:"sites_per_node"`
	Interval  string          `json:"interval"`
	Duration  string          `json:"duration"`
	StartedAt string          `json:"started_at"`
	Fleet     fleetStats      `json:"fleet"`
	Fanout    fanoutStats     `json:"fanout"`
	REST      restStats       `json:"rest"`
	IngestCPU *cpuSample      `json:"ingest_cpu,omitempty"`
	History   *historyStats   `json:"history,omitempty"`
	Freshness freshnessReport `json:"freshness_chaos,omitempty"`
	Dropped   bool            `json:"chaos_node_dropped"`
	Resumed   bool            `json:"chaos_resume_ok"`
	Notes     []string        `json:"notes,omitempty"`
}

type fleetStats struct {
	Nodes       int     `json:"nodes"`
	FramesSent  int64   `json:"frames_sent"`
	FramesAcked int64   `json:"frames_acked"`
	BytesSent   int64   `json:"bytes_sent"`
	Errors      int     `json:"errors"`
	Resumes     int     `json:"resumes"`
	AckLatAvgMS float64 `json:"ack_latency_avg_ms"`
	AckLatMaxMS float64 `json:"ack_latency_max_ms"`
}

type fanoutStats struct {
	Count   int     `json:"matched"`
	Pending int     `json:"unmatched_pending"`
	P50ms   float64 `json:"p50_ms"`
	P95ms   float64 `json:"p95_ms"`
	P99ms   float64 `json:"p99_ms"`
	MaxMs   float64 `json:"max_ms"`
}

type restStats struct {
	HealthCount   int     `json:"healthz_n"`
	HealthP50ms   float64 `json:"healthz_p50_ms"`
	HealthP99ms   float64 `json:"healthz_p99_ms"`
	FleetCount    int     `json:"fleet_n"`
	FleetP50ms    float64 `json:"fleet_p50_ms"`
	FleetP99ms    float64 `json:"fleet_p99_ms"`
	FleetMaxMs    float64 `json:"fleet_max_ms"`
	FleetAvgBytes int64   `json:"fleet_avg_bytes"`
	FleetMaxBytes int64   `json:"fleet_max_bytes"`
}

type cpuSample struct {
	Wall              string  `json:"wall"`
	CPUPercent        float64 `json:"cpu_percent_of_one_core"`
	CPUPercentCore    float64 `json:"cpu_percent_of_machine"`
	Cores             int     `json:"cores"`
	RSSStartMB        int     `json:"rss_start_mb"`
	RSSEndMB          int     `json:"rss_end_mb"`
	FramesPerSecond   float64 `json:"frames_acked_per_second"`
	CPUsPer1000Frames float64 `json:"cpu_microseconds_per_1000_frames"`
}

type historyStats struct {
	Rows       int64    `json:"rows_written_during_run"`
	RowsPerSec float64  `json:"rows_per_second"`
	MaxLag     durStr   `json:"max_insert_lag"`
	Decimated  bool     `json:"drain_decimated"`
	Expected   int64    `json:"expected_rows_if_every_sample_persisted"`
	Notes      []string `json:"notes,omitempty"`
}

type freshnessReport struct {
	Observed      bool   `json:"observed"`
	DroppedAt     string `json:"dropped_at,omitempty"`
	StaleAt       string `json:"stale_at,omitempty"`
	OfflineAt     string `json:"offline_at,omitempty"`
	Final         string `json:"final_state"`
	LiveAfterDrop bool   `json:"live_after_drop"`
	Samples       int    `json:"state_polls"`
}

type durStr struct{ time.Duration }

func (d durStr) MarshalJSON() ([]byte, error) {
	if d.Duration == 0 {
		return []byte(`"0s"`), nil
	}
	return []byte(`"` + d.Truncate(time.Millisecond).String() + `"`), nil
}

func buildReport(r report) report {
	if r.Fleet.Nodes == 0 && r.Nodes > 0 {
		r.Fleet.Nodes = r.Nodes
	}
	return r
}

func pctl(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(float64(len(sorted)) * p)
	if i >= len(sorted) {
		i = len(sorted) - 1
	}
	if i < 0 {
		i = 0
	}
	return sorted[i]
}

func sortFloats(v []float64) { sort.Float64s(v) }

var _ = time.Now
