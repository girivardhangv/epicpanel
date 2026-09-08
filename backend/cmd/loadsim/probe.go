package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"sync"
	"time"
)

// restProbe measures request-path latency against the running control plane
// while the fleet streams: healthz (no DB) and the live fleet metrics
// projection (in-memory read at fleet scale).
type restProbe struct {
	base      string
	admin     *adminClient
	orgID     string
	every     time.Duration
	mu        sync.Mutex
	healthMS  []float64
	fleetMS   []float64
	fleetB    []int64
	stopOnce  sync.Once
	stopCh    chan struct{}
}

func newRESTProbe(base string, admin *adminClient, orgID string, every time.Duration) *restProbe {
	return &restProbe{base: base, admin: admin, orgID: orgID, every: every, stopCh: make(chan struct{})}
}

func (p *restProbe) start(ctx context.Context) {
	if p.every <= 0 {
		return
	}
	go func() {
		t := time.NewTicker(p.every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-p.stopCh:
				return
			case <-t.C:
				p.probe()
			}
		}
	}()
}

func (p *restProbe) probe() {
	t0 := time.Now()
	code, _, err := p.admin.get("/healthz")
	d := time.Since(t0)
	if err == nil && code == 200 {
		p.mu.Lock()
		p.healthMS = append(p.healthMS, msFloat(d))
		p.mu.Unlock()
	}

	t0 = time.Now()
	code, body, err := p.admin.fleetFleetMetrics(p.orgID)
	d = time.Since(t0)
	if err == nil && code == 200 {
		p.mu.Lock()
		p.fleetMS = append(p.fleetMS, msFloat(d))
		p.fleetB = append(p.fleetB, int64(len(body)))
		p.mu.Unlock()
	}
}

func (p *restProbe) stop() {
	p.stopOnce.Do(func() { close(p.stopCh) })
	// Capture the final snapshot under lock.
}

func (p *restProbe) stats() restStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	var st restStats
	sortFloats(p.healthMS)
	sortFloats(p.fleetMS)
	st.HealthCount = len(p.healthMS)
	st.HealthP50ms = pctl(p.healthMS, 0.50)
	st.HealthP99ms = pctl(p.healthMS, 0.99)
	st.FleetCount = len(p.fleetMS)
	st.FleetP50ms = pctl(p.fleetMS, 0.50)
	st.FleetP99ms = pctl(p.fleetMS, 0.99)
	if n := len(p.fleetMS); n > 0 {
		st.FleetMaxMs = p.fleetMS[n-1]
		var total int64
		for _, b := range p.fleetB {
			total += b
			if b > st.FleetMaxBytes {
				st.FleetMaxBytes = b
			}
		}
		st.FleetAvgBytes = total / int64(n)
	}
	return st
}

// procSample is one /proc/<pid>/stat read of the api process.
type procSample struct {
	ok    bool
	at    time.Time
	ticks uint64 // utime+stime in clock ticks
	rssMB int
}

// sampleProc reads utime+stime+rss for pid (0 => disabled). The simulator
// measures the control plane's CPU cost of ingest from outside the process
// so nothing it measures is perturbed by its own instrumentation.
func sampleProc(pid int) procSample {
	if pid <= 0 {
		return procSample{}
	}
	b, err := os.ReadFile("/proc/" + itoa(pid) + "/stat")
	if err != nil {
		return procSample{}
	}
	// stat: pid (comm) state ppid ... field 14 utime, 15 stime, 24 rss(pages).
	// comm may contain spaces — split after the closing parenthesis.
	i := bytes.LastIndexByte(b, ')')
	if i < 0 || i+2 > len(b) {
		return procSample{}
	}
	fields := bytes.Fields(b[i+2:])
	// After the paren: field index 0 = state (field 3). utime = field 11,
	// stime = 12, rss = 21 (0-based within this slice).
	if len(fields) < 22 {
		return procSample{}
	}
	ut := parseUint(fields[11])
	st := parseUint(fields[12])
	rssPages := parseUint(fields[21])
	return procSample{
		ok:    true,
		at:    time.Now(),
		ticks: ut + st,
		rssMB: int(rssPages * uint64(pageSize) / (1 << 20)),
	}
}

const pageSize = 4096

func parseUint(b []byte) uint64 {
	var n uint64
	for _, c := range b {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + uint64(c-'0')
	}
	return n
}

// limitReaderShim exists so the file compiles without unused imports when
// probes evolve; io is used by admin.go's shared client.
var _ io.Reader
