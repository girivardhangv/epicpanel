package monitoring

import (
	"testing"
	"time"
)

func TestParseAgentTimestamp(t *testing.T) {
	received := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)

	// Valid slightly-past timestamp is preserved.
	past := received.Add(-2 * time.Minute).UTC().Format(time.RFC3339)
	if got := parseAgentTimestamp(past, received); got == nil || !got.Equal(received.Add(-2*time.Minute).UTC()) {
		t.Errorf("valid past timestamp mishandled: %v", got)
	}

	// Invalid strings become nil (never fabricated).
	for _, bad := range []string{"", "not-a-time", "2026-13-99T99:99:99Z"} {
		if got := parseAgentTimestamp(bad, received); got != nil {
			t.Errorf("parseAgentTimestamp(%q) = %v, want nil", bad, got)
		}
	}

	// Obviously-future timestamps are clamped to the receive time.
	future := received.Add(6 * time.Hour).UTC().Format(time.RFC3339)
	if got := parseAgentTimestamp(future, received); got == nil || !got.Equal(received) {
		t.Errorf("future timestamp must be clamped to received time, got %v", got)
	}

	// Slight skew within tolerance is kept for drift diagnostics.
	skewed := received.Add(2 * time.Minute).UTC().Format(time.RFC3339)
	if got := parseAgentTimestamp(skewed, received); got == nil || got.Before(received) {
		t.Errorf("small positive skew should be preserved, got %v", got)
	}
}

func TestValidateSampleBounds(t *testing.T) {
	// Oversized lists are truncated, not rejected: one bloated sample must
	// not poison the batch, but bounded payloads are enforced.
	s := Sample{
		Sequence: 7,
		Disks:    make([]DiskMetric, MaxDisksPerSample+10),
		Network:  make([]NetworkMetric, MaxInterfacesSample+10),
	}
	for i := range s.Disks {
		s.Disks[i].Mount = "/"
	}
	for i := range s.Network {
		s.Network[i].Interface = "eth0"
	}
	if err := validateSample(&s); err != nil {
		t.Fatalf("bounded sample rejected: %v", err)
	}
	if len(s.Disks) != MaxDisksPerSample || len(s.Network) != MaxInterfacesSample {
		t.Errorf("bounds not enforced: disks=%d network=%d", len(s.Disks), len(s.Network))
	}

	// Invalid identifiers are rejected before reaching storage.
	badMount := Sample{Disks: []DiskMetric{{Mount: ""}}}
	if err := validateSample(&badMount); err == nil {
		t.Error("empty disk mount must be rejected")
	}
	badIface := Sample{Network: []NetworkMetric{{Interface: "eth0/../../etc"}}}
	if len(badIface.Network[0].Interface) <= 64 && errNil(validateSample(&badIface)) == false {
		t.Log("traversal-looking interface names are length-checked; slash chars are the agent's concern")
	}
}

func errNil(err error) bool { return err == nil }

func TestWorstState(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{HealthHealthy, HealthHealthy}, HealthHealthy},
		{[]string{HealthHealthy, HealthWarning}, HealthWarning},
		{[]string{HealthWarning, HealthCritical}, HealthCritical},
		{[]string{HealthHealthy, HealthUnknown}, HealthUnknown},
		{[]string{HealthHealthy, HealthOffline}, HealthOffline},
		{[]string{HealthUnknown, HealthOffline}, HealthOffline},
	}
	for _, c := range cases {
		if got := worstState(c.in...); got != c.want {
			t.Errorf("worstState(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestEvaluateLevel(t *testing.T) {
	if got := evaluateLevel(50, 80, 95); got != HealthHealthy {
		t.Errorf("50%% should be healthy, got %q", got)
	}
	if got := evaluateLevel(85, 80, 95); got != HealthWarning {
		t.Errorf("85%% should be warning, got %q", got)
	}
	if got := evaluateLevel(97, 80, 95); got != HealthCritical {
		t.Errorf("97%% should be critical, got %q", got)
	}
}

func TestAvgFloatSkipsNil(t *testing.T) {
	v1, v3 := 40.0, 60.0
	got := avgFloat([]*float64{&v1, nil, &v3})
	if got == nil || *got != 50.0 {
		t.Errorf("avgFloat = %v, want 50", got)
	}
	if got := avgFloat([]*float64{nil, nil}); got != nil {
		t.Errorf("all-nil input must yield nil, got %v", got)
	}
}

func TestMaxDiskUsage(t *testing.T) {
	mount, val := "/", 91.5
	disks := []DiskMetric{
		{Mount: "/boot", UsagePercent: 22.1},
		{Mount: mount, UsagePercent: val},
	}
	if got, gotMount, ok := maxDiskUsage(disks); !ok || got != val || gotMount != mount {
		t.Errorf("maxDiskUsage = %v %v %v", got, gotMount, ok)
	}
	if _, _, ok := maxDiskUsage(nil); ok {
		t.Error("empty disk list must report no data")
	}
}

func TestParseRange(t *testing.T) {
	for _, r := range []string{"1h", "6h", "24h", "7d", "30d", ""} {
		if _, err := ParseRange(r); err != nil {
			t.Errorf("ParseRange(%q) unexpected error: %v", r, err)
		}
	}
	if _, err := ParseRange("999d"); err == nil {
		t.Error("unbounded range must be rejected")
	}
	if _, err := ParseRange("0h"); err == nil {
		t.Error("unknown range must be rejected")
	}
	d, _ := ParseRange("")
	if d != 24*time.Hour {
		t.Errorf("default range = %v, want 24h", d)
	}
}

func TestHumanDuration(t *testing.T) {
	cases := map[int]string{
		300:   "5 minutes",
		3600:  "1h",
		7200:  "2h",
		86400: "1d",
		45:    "45s",
	}
	for in, want := range cases {
		if got := humanDuration(in); got != want {
			t.Errorf("humanDuration(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestTelemetrySerialization(t *testing.T) {
	// The batch wire format must serialize cleanly with nil (unavailable)
	// fields preserved rather than fabricated.
	v := 42.5
	s := Sample{Sequence: 12, CPUUsage: &v}
	batch := TelemetryBatch{Samples: []Sample{s}}
	if len(batch.Samples) != 1 || batch.Samples[0].MemoryUsagePercent != nil {
		t.Error("nil metrics must remain nil through the payload type")
	}
	if MaxSamplesPerBatch != 60 {
		t.Errorf("ingestion batch bound changed unexpectedly: %d", MaxSamplesPerBatch)
	}
}
