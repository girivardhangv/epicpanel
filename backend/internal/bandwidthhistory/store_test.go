package bandwidthhistory

import (
	"testing"
	"time"

	"github.com/epicbyte/epicpanel/backend/internal/agentproto"
)

// TestGroupWindows covers the bucket math: same-hour windows fold into one
// additive row, empty ids are dropped, and the receive-time hour is used.
func TestGroupWindows(t *testing.T) {
	recv := time.Date(2026, 9, 25, 18, 42, 5, 0, time.UTC)
	frames := []agentproto.SiteTraffic{
		{WebsiteID: "w1", Bytes: 1000, Requests: 10},
		{WebsiteID: "w1", Bytes: 250, Requests: 2},
		{WebsiteID: "w2", Bytes: 7, Requests: 1},
		{WebsiteID: "", Bytes: 999, Requests: 9}, // dropped
	}
	rows := groupWindows(frames, recv)
	if len(rows) != 2 {
		t.Fatalf("rows: %+v", rows)
	}
	byID := map[string]sampleRow{}
	for _, r := range rows {
		byID[r.WebsiteID] = r
	}
	if got := byID["w1"]; got.TxBytes != 1250 || got.Requests != 12 {
		t.Fatalf("w1 fold: %+v", got)
	}
	if !byID["w1"].Hour.Equal(recv.Truncate(time.Hour)) {
		t.Fatalf("hour bucket: %v", byID["w1"].Hour)
	}
	if rows := groupWindows(nil, recv); len(rows) != 0 {
		t.Fatalf("empty frames must yield no rows")
	}
}
