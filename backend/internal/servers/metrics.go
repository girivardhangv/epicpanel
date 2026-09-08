package servers

import "time"

type Metrics struct {
	CPUPercent  float64   `json:"cpu_percent"`
	MemoryTotal int64     `json:"memory_total_bytes"`
	MemoryUsed  int64     `json:"memory_used_bytes"`
	DiskTotal   int64     `json:"disk_total_bytes"`
	DiskUsed    int64     `json:"disk_used_bytes"`
	Load1       float64   `json:"load1"`
	Load5       float64   `json:"load5"`
	Load15      float64   `json:"load15"`
	CollectedAt time.Time `json:"collected_at"`
}
