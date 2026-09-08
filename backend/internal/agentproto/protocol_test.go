package agentproto

import (
	"testing"
	"time"
)

func TestFreshnessThresholds(t *testing.T) {
	cases := []struct {
		age  time.Duration
		have bool
		want string
	}{
		{0, true, "LIVE"},
		{5 * time.Second, true, "LIVE"},
		{LiveMaxAge, true, "LIVE"},
		{LiveMaxAge + 10*time.Millisecond, true, "STALE"},
		{30 * time.Second, true, "STALE"},
		{StaleMaxAge, true, "STALE"},
		{StaleMaxAge + time.Second, true, "OFFLINE"},
		{0, false, "OFFLINE"},
		{time.Hour, false, "OFFLINE"},
	}
	for _, c := range cases {
		if got := Freshness(c.age, c.have); got != c.want {
			t.Errorf("Freshness(%v, %v) = %q, want %q", c.age, c.have, got, c.want)
		}
	}
}
