package licensing

import (
	"testing"
	"time"
)

var futureExpiry = time.Now().UTC().Add(365 * 24 * time.Hour)

func TestPolicyMatrix(t *testing.T) {
	hourAgo := time.Now().UTC().Add(-1 * time.Hour)
	dayAgo := time.Now().UTC().Add(-24 * time.Hour)
	neverValidated := (*time.Time)(nil)

	p := Policy{GraceEnabled: true, GracePeriod: 72 * time.Hour}
	now := time.Now().UTC()

	cases := []struct {
		name       string
		hasLicense bool
		status     string
		lastVal    *time.Time
		expiresAt  *time.Time
		wantUsable bool
		wantState  string
	}{
		{"active license validated recently", true, StatusActive, &hourAgo, &futureExpiry, true, StatusActive},
		{
			// Stored 'grace' row (set by an outage path) stays usable while
			// its last successful validation is inside the window.
			"stored grace row remains usable within window",
			true, StatusGrace, &dayAgo, &futureExpiry, true, StatusGrace,
		},
		{
			"grace expires after window",
			true, StatusActive, func() *time.Time { t := now.Add(-73 * time.Hour); return &t }(), &futureExpiry,
			false, StatusInvalid,
		},
		{"no license at all", false, "", neverValidated, nil, false, StatusInactive},
		{"inactive stored row", true, StatusInactive, &hourAgo, nil, false, StatusInactive},
		{"expired by date beats active flag", true, StatusActive, &hourAgo, func() *time.Time { x := now.Add(-time.Hour); return &x }(), false, StatusExpired},
		{"suspended blocks even in grace", true, StatusSuspended, &hourAgo, &futureExpiry, false, StatusSuspended},
		{"invalid status blocks", true, StatusInvalid, &hourAgo, &futureExpiry, false, StatusInvalid},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := p.Evaluate(tc.hasLicense, tc.status, tc.lastVal, tc.expiresAt, now)
			if got.Usable != tc.wantUsable || got.State != tc.wantState {
				t.Fatalf("Evaluate(...) = {Usable:%v State:%s}, want {Usable:%v State:%s}",
					got.Usable, got.State, tc.wantUsable, tc.wantState)
			}
		})
	}
}

func TestPolicyGraceDisabled(t *testing.T) {
	p := Policy{GraceEnabled: false}
	hourAgo := time.Now().UTC().Add(-1 * time.Hour)
	d := p.Evaluate(true, StatusActive, &hourAgo, &futureExpiry, time.Now().UTC())
	if d.Usable != true || d.State != StatusActive {
		t.Fatalf("with grace disabled an explicitly active license should still be usable, got %+v", d)
	}
}

func TestLastValidatedStale(t *testing.T) {
	p := Policy{}
	now := time.Now().UTC()
	fiveHoursAgo := now.Add(-5 * time.Hour)

	if p.LastValidatedStale(&fiveHoursAgo, 6*time.Hour, now) {
		t.Fatal("should not be stale when last validation < interval")
	}
	if !p.LastValidatedStale(&fiveHoursAgo, 4*time.Hour, now) {
		t.Fatal("stale when last validation > interval")
	}
	if p.LastValidatedStale(nil, time.Hour, now) {
		t.Fatal("nil timestamp cannot be stale")
	}
}
