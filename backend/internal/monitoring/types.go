// Package monitoring implements telemetry ingestion, storage, historical
// aggregation, health states and the alert foundation.
//
// Design constraints honored throughout:
//   - The agent's clock is never trusted for ordering; server_received_at is
//     authoritative. Agent timestamps are stored for drift diagnostics only.
//   - All list-shaped payloads (disks, interfaces, processes, services) are
//     hard-bounded at ingestion.
//   - Health and alerts are derived from persisted samples with durations —
//     a single noisy sample never changes a server's state or fires an alert.
package monitoring

import "time"

// DiskMetric is one filesystem sample.
type DiskMetric struct {
	Mount        string  `json:"mount"`
	Filesystem   string  `json:"fs,omitempty"`
	TotalBytes   int64   `json:"total_bytes"`
	UsedBytes    int64   `json:"used_bytes"`
	FreeBytes    int64   `json:"free_bytes"`
	UsagePercent float64 `json:"usage_percent"`
}

// NetworkMetric is one interface's cumulative counters (rates are derived
// centrally from consecutive samples; the agent never computes rates).
type NetworkMetric struct {
	Interface string  `json:"interface"`
	RxBytes   float64 `json:"rx_bytes"`
	TxBytes   float64 `json:"tx_bytes"`
	RxPackets float64 `json:"rx_packets"`
	TxPackets float64 `json:"tx_packets"`
	Errors    float64 `json:"errors"`
	Drops     float64 `json:"drops"`
}

// ProcessMetric is one bounded process entry.
type ProcessMetric struct {
	Name        string   `json:"name"`
	PID         int32    `json:"pid"`
	CPUPercent  *float64 `json:"cpu_percent"` // nil = unavailable this cycle
	MemoryBytes uint64   `json:"memory_bytes"`
	Status      string   `json:"status"`
}

// ServiceHealth mirrors the spec's generic service model.
type ServiceHealth struct {
	Name        string  `json:"name"`
	DisplayName string  `json:"display_name"`
	Status      string  `json:"status"`   // Running | Stopped | Failed | Unknown | NotInstalled
	Running     bool    `json:"running"`
	Enabled     *bool   `json:"enabled"`  // nil = not determinable on this platform
	LastChecked string  `json:"last_checked,omitempty"`
}

// Service status vocabulary.
const (
	ServiceRunning     = "Running"
	ServiceStopped     = "Stopped"
	ServiceFailed      = "Failed"
	ServiceUnknown     = "Unknown"
	ServiceNotInstalled = "NotInstalled"
)

// Sample is the normalized telemetry payload element sent by the agent.
// Nil floats/ints mean "unavailable on this platform" — never fabricated.
type Sample struct {
	Timestamp string `json:"timestamp"` // agent clock, RFC3339 UTC (informational)
	Sequence  int64  `json:"sequence"`
	AgentVer  string `json:"agent_version,omitempty"`

	CPUUsage   *float64 `json:"cpu_usage_percent"`
	CPUUser    *float64 `json:"cpu_user_percent"`
	CPUSystem  *float64 `json:"cpu_system_percent"`
	CPUIdle    *float64 `json:"cpu_idle_percent"`
	Load1      *float64 `json:"load_1m"`
	Load5      *float64 `json:"load_5m"`
	Load15     *float64 `json:"load_15m"`

	MemoryTotalBytes     *int64   `json:"memory_total_bytes"`
	MemoryUsedBytes      *int64   `json:"memory_used_bytes"`
	MemoryAvailableBytes *int64   `json:"memory_available_bytes"`
	MemoryFreeBytes      *int64   `json:"memory_free_bytes"`
	MemoryUsagePercent   *float64 `json:"memory_usage_percent"`
	SwapTotalBytes       *int64   `json:"swap_total_bytes"`
	SwapUsedBytes        *int64   `json:"swap_used_bytes"`
	SwapUsagePercent     *float64 `json:"swap_usage_percent"`

	UptimeSeconds *int64 `json:"uptime_seconds"`

	Disks     []DiskMetric     `json:"disk"`
	Network   []NetworkMetric  `json:"network"`
	Processes []ProcessMetric  `json:"processes"`
	Services  []ServiceHealth  `json:"services"`
	Errors    map[string]string `json:"monitoring_errors,omitempty"`
}

// TelemetryBatch is the ingestion request body.
type TelemetryBatch struct {
	Samples []Sample `json:"samples"`
}

// Ingestion bounds (defense in depth against oversized/abusive payloads).
const (
	MaxSamplesPerBatch   = 60
	MaxDisksPerSample    = 24
	MaxInterfacesSample  = 32
	MaxProcessesPerSample = 20
	MaxServicesPerSample = 32
	MaxAgentClockSkew    = 10 * time.Minute // future timestamps beyond this are clamped
)

// Health states.
const (
	HealthHealthy  = "healthy"
	HealthWarning  = "warning"
	HealthCritical = "critical"
	HealthUnknown  = "unknown"
	HealthOffline  = "offline"
)

// Thresholds holds configurable warning/critical percentages. Defaults per
// spec; operators may override through system settings.
type Thresholds struct {
	CPUWarn, CPUCrit       float64
	MemWarn, MemCrit       float64
	DiskWarn, DiskCrit     float64
	OfflineAfterSeconds    int
}

func DefaultThresholds() Thresholds {
	return Thresholds{
		CPUWarn: 80, CPUCrit: 95,
		MemWarn: 80, MemCrit: 95,
		DiskWarn: 80, DiskCrit: 90,
		OfflineAfterSeconds: 300,
	}
}

// HealthPoint is one component's evaluated state.
type HealthPoint struct {
	Component string   `json:"component"`
	Value     *float64 `json:"value"` // current (smoothed) value; nil = unknown
	State     string   `json:"state"`
}

// HealthState is the normalized server health used by UI and alerts.
type HealthState struct {
	State     string        `json:"state"`
	Points    []HealthPoint `json:"points"`
	Basis     int           `json:"basis"` // number of samples the evaluation averaged
}

// worstState folds component states into the overall server state.
// Precedence: offline > unknown > critical > warning > healthy.
func worstState(states ...string) string {
	worst := HealthHealthy
	for _, s := range states {
		switch s {
		case HealthOffline:
			return HealthOffline
		case HealthUnknown:
			worst = HealthUnknown
		case HealthCritical:
			if worst != HealthUnknown {
				worst = HealthCritical
			}
		case HealthWarning:
			if worst == HealthHealthy {
				worst = HealthWarning
			}
		}
	}
	return worst
}

// evaluateLevel maps a value against warn/crit thresholds.
func evaluateLevel(v, warn, crit float64) string {
	switch {
	case v > crit:
		return HealthCritical
	case v > warn:
		return HealthWarning
	default:
		return HealthHealthy
	}
}

// maxDiskUsage returns the highest disk usage percent in a sample.
func maxDiskUsage(disks []DiskMetric) (float64, string, bool) {
	var (
		max   = -1.0
		mount string
	)
	for _, d := range disks {
		if d.UsagePercent > max {
			max = d.UsagePercent
			mount = d.Mount
		}
	}
	if max < 0 {
		return 0, "", false
	}
	return max, mount, true
}

// avg smooths a metric over the last n values (nil values skipped).
func avgFloat(values []*float64) *float64 {
	var sum float64
	var n int
	for _, v := range values {
		if v != nil {
			sum += *v
			n++
		}
	}
	if n == 0 {
		return nil
	}
	out := sum / float64(n)
	return &out
}
