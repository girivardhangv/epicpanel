// Command loadsim simulates a fleet of EpicPanel node agents speaking the
// Phase 3 real-time metrics protocol (agentproto v1) against a control
// plane, and measures what the scale report needs:
//
//   - ingest throughput (frames acked / s) and ack latency
//   - WebSocket fan-out latency (agent frame sent -> /v1/ws metrics frame
//     received by a browser-authenticated client)
//   - control-plane ingest CPU (sampled from /proc of the api process)
//   - REST read latency under load (fleet metrics + healthz)
//   - historical write rate (server_metrics rows, via -db-url)
//   - freshness honesty under chaos (-drop-node-at: LIVE -> STALE -> OFFLINE
//     transitions measured live; never stale-as-live)
//
// Wire format reference: backend/internal/api/agentstream_test.go and
// backend/internal/agentproto/protocol.go.
//
// Flags (the four from the phase brief first):
//
//	-nodes N        simulated agents (default 10)
//	-sites N        SiteSample envelopes per frame (default 4)
//	-interval D     sample cadence per node (default 5s)
//	-duration D     run length (default 60s)
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"
)

const version = "1.0.0"

func main() {
	var (
		api        = flag.String("api", "http://127.0.0.1:8080", "control plane base URL")
		nodes      = flag.Int("nodes", 10, "number of simulated agent nodes")
		sites      = flag.Int("sites", 4, "per-site envelopes carried by each frame")
		interval   = flag.Duration("interval", 5*time.Second, "per-node sample cadence")
		duration   = flag.Duration("duration", 60*time.Second, "how long the fleet streams")
		adminEmail = flag.String("admin-email", "loadsim@epicpanel.test", "admin account email (bootstrapped via first-user register when absent)")
		adminPass  = flag.String("admin-password", "loadsim-admin-passphrase", "admin account password")
		orgName    = flag.String("org", "loadsim-fleet", "organization created/reused for the simulated fleet")
		fanout     = flag.Int("fanout", 1, "browser WS clients measuring fan-out latency")
		apiPID     = flag.Int("api-pid", 0, "pid of the epicpanel-api process for CPU/RSS sampling (0 = skip)")
		dbURL      = flag.String("db-url", "", "postgres URL for server_metrics row accounting (optional)")
		dropNodeAt = flag.Duration("drop-node-at", 0, "chaos: hard-drop node 0's stream after this long and measure LIVE->STALE->OFFLINE (0 = off)")
		resumeAt   = flag.Duration("resume-chaos-at", 0, "chaos: disconnect+resume node 1's stream at this point (ring replay, 0 = off)")
		probeEvery = flag.Duration("probe-every", 2*time.Second, "REST read-probe cadence")
		reportPath = flag.String("report", "", "write the JSON report to this file (summary always printed)")
		runLabel   = flag.String("label", "", "free-form label stored in the report")
		quiet      = flag.Bool("quiet", false, "print only the final summary")
	)
	flag.Parse()

	if err := run(*api, *nodes, *sites, *interval, *duration, *adminEmail, *adminPass,
		*orgName, *fanout, *apiPID, *dbURL, *dropNodeAt, *resumeAt, *probeEvery,
		*reportPath, *runLabel, *quiet); err != nil {
		fmt.Fprintf(os.Stderr, "loadsim: %v\n", err)
		os.Exit(1)
	}
}

func run(api string, nNodes, nSites int, interval, duration time.Duration,
	email, pass, orgName string, nFanout, apiPID int, dbURL string,
	dropNodeAt, resumeAt, probeEvery time.Duration, reportPath, label string, quiet bool) error {

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	admin, err := newAdminClient(api, email, pass)
	if err != nil {
		return err
	}
	orgID, err := admin.ensureOrg(orgName)
	if err != nil {
		return fmt.Errorf("ensure org: %w", err)
	}
	if !quiet {
		fmt.Printf("[loadsim] org %s ready; enrolling %d nodes...\n", orgID, nNodes)
	}

	tokens, err := admin.enrollFleet(orgID, nNodes, quiet)
	if err != nil {
		return fmt.Errorf("enroll fleet: %w", err)
	}

	fleet := newFleet(api, tokens, nSites, interval)
	fan := newFanoutProbes(api, admin, nFanout)
	rest := newRESTProbe(api, admin, orgID, probeEvery)
	fresh := newFreshnessWatcher(api, admin, orgID)

	started := time.Now()

	// Browser-side WS clients come up first so they never miss early frames.
	if err := fan.start(ctx); err != nil {
		return fmt.Errorf("fan-out clients: %w", err)
	}
	if err := fleet.start(ctx, fan); err != nil {
		return fmt.Errorf("start fleet: %w", err)
	}
	rest.start(ctx)

	cpu0 := sampleProc(apiPID)

	// Chaos schedules.
	var dropReport freshnessReport
	dropped := false
	if dropNodeAt > 0 {
		go func() {
			t := time.NewTimer(dropNodeAt)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				fleet.dropHard(0)
				dropped = true
				if !quiet {
					fmt.Printf("[loadsim] chaos: node 0 stream hard-dropped at t=%s\n", time.Since(started).Round(time.Millisecond))
				}
				dropReport = fresh.watch(ctx, tokens[0].ServerID, started)
			}
		}()
	}
	var resumed bool
	if resumeAt > 0 {
		go func() {
			t := time.NewTimer(resumeAt)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				fleet.dropHard(1)
				if !quiet {
					fmt.Printf("[loadsim] chaos: node 1 disconnected at t=%s (resuming from ring)\n", time.Since(started).Round(time.Millisecond))
				}
				resumed = fleet.resume(1)
			}
		}()
	}

	runCtx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	<-runCtx.Done()
	// Give the tail (acks, broadcasts, final REST probe) a beat to land.
	time.Sleep(500 * time.Millisecond)
	elapsed := time.Since(started)

	cpu1 := sampleProc(apiPID)
	fleet.stop()
	fan.stop()
	rest.stop()

	rep := buildReport(report{
		Label:     label,
		Version:   version,
		API:       api,
		Nodes:     nNodes,
		Sites:     nSites,
		Interval:  interval.String(),
		Duration:  elapsed.Truncate(time.Millisecond).String(),
		StartedAt: started.UTC().Format(time.RFC3339Nano),
		Fleet:     fleet.stats(),
		Fanout:    fan.stats(),
		REST:      rest.stats(),
	})
	rep.Freshness = dropReport
	rep.Dropped = dropped
	rep.Resumed = resumed
	if dbURL != "" {
		rep.History = measureHistory(dbURL, started)
	}
	if cpu0.ok && cpu1.ok {
		wall := cpu1.at.Sub(cpu0.at)
		cpuSeconds := float64(cpu1.ticks-cpu0.ticks) / 100.0
		procs := runtime.GOMAXPROCS(0)
		per1000 := 0.0
		if n := rep.Fleet.FramesAcked; n > 0 {
			per1000 = cpuSeconds / float64(n) * 1000 * 1000 // µs of CPU per 1000 frames
		}
		rep.IngestCPU = &cpuSample{
			Wall:              wall.Truncate(time.Millisecond).String(),
			CPUPercent:        cpuSeconds / wall.Seconds() * 100,
			CPUPercentCore:    cpuSeconds / (wall.Seconds() * float64(procs)) * 100,
			Cores:             procs,
			RSSStartMB:        cpu0.rssMB,
			RSSEndMB:          cpu1.rssMB,
			FramesPerSecond:   float64(rep.Fleet.FramesAcked) / wall.Seconds(),
			CPUsPer1000Frames: per1000,
		}
	}

	printReport(rep, quiet)
	if reportPath != "" {
		b, _ := json.MarshalIndent(rep, "", "  ")
		if err := os.WriteFile(reportPath, b, 0o600); err != nil {
			return fmt.Errorf("write report: %w", err)
		}
		if !quiet {
			fmt.Printf("[loadsim] report written to %s\n", reportPath)
		}
	}
	return nil
}

func printReport(r report, quiet bool) {
	b, _ := json.MarshalIndent(r, "", "  ")
	fmt.Println(string(b))
	if quiet {
		return
	}
	var l []string
	if r.Fanout.Count > 0 {
		l = append(l,
			fmt.Sprintf("fan-out latency p50=%.2fms p95=%.2fms p99=%.2fms max=%.2fms (n=%d)",
				r.Fanout.P50ms, r.Fanout.P95ms, r.Fanout.P99ms, r.Fanout.MaxMs, r.Fanout.Count))
	}
	if r.REST.FleetCount > 0 {
		l = append(l,
			fmt.Sprintf("REST fleet-metrics p50=%.2fms p99=%.2fms max=%.2fms (n=%d, avg %s/frame)",
				r.REST.FleetP50ms, r.REST.FleetP99ms, r.REST.FleetMaxMs, r.REST.FleetCount, humanBytes(r.REST.FleetAvgBytes)))
		l = append(l, fmt.Sprintf("REST healthz     p50=%.2fms p99=%.2fms (n=%d)", r.REST.HealthP50ms, r.REST.HealthP99ms, r.REST.HealthCount))
	}
	if r.IngestCPU != nil {
		l = append(l, fmt.Sprintf("ingest CPU %.1f%% of one core (%.1f%% of %d cores), %.0f CPU-µs/1000 frames, RSS %d->%d MB",
			r.IngestCPU.CPUPercent, r.IngestCPU.CPUPercentCore, r.IngestCPU.Cores,
			r.IngestCPU.CPUsPer1000Frames, r.IngestCPU.RSSStartMB, r.IngestCPU.RSSEndMB))
	}
	if r.History != nil {
		l = append(l, fmt.Sprintf("history rows written during run: %d (%.1f rows/s), max insert lag %s",
			r.History.Rows, r.History.RowsPerSec, r.History.MaxLag))
	}
	if r.Freshness.Observed {
		l = append(l, fmt.Sprintf("freshness (dropped node): LIVE->STALE at %s, STALE->OFFLINE at %s (final %s, never stale-as-live: %v)",
			r.Freshness.StaleAt, r.Freshness.OfflineAt, r.Freshness.Final, !r.Freshness.LiveAfterDrop))
	}
	for _, s := range l {
		fmt.Printf("  %s\n", s)
	}
}

func humanBytes(n int64) string {
	const kb = 1024
	switch {
	case n >= kb*kb:
		return fmt.Sprintf("%.1fMiB", float64(n)/(kb*kb))
	case n >= kb:
		return fmt.Sprintf("%.1fKiB", float64(n)/kb)
	default:
		return fmt.Sprintf("%dB", n)
	}
}

func trimSlash(s string) string { return strings.TrimSuffix(s, "/") }
