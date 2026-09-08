package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"time"

	"github.com/epicbyte/epicpanel/backend/internal/agentproto"
)

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}
	return b
}

func jsonMarshal(v any) ([]byte, error)   { return json.Marshal(v) }
func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

func errStr(s string) error { return errors.New(s) }

func msFloat(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

// wsURL converts an http(s) base URL into the /v1/agent/stream ws URL
// (mirrors internal/agent/stream.go).
func wsURL(base string) string {
	ws := "ws" + trimSlash(base)[4:]
	if len(base) > 5 && base[4] == 's' { // https -> wss
		ws = "wss" + trimSlash(base)[5:]
	}
	return ws + "/v1/agent/stream"
}

// syntheticSample builds a protocol-realistic Sample: node envelope with
// plausible magnitudes plus n per-site envelopes whose IDs are stable per
// node so the control plane's per-site maps stay consistent.
func syntheticSample(nodeIdx, sites int, ts time.Time) agentproto.Sample {
	r := rand.New(rand.NewSource(int64(nodeIdx)*1_000_003 + ts.UnixMilli()))
	cores := 8
	s := agentproto.Sample{
		Node: agentproto.NodeSample{
			CPUPercent:      clamp(20+r.Float64()*60, 0, 100),
			CPUCores:        float64(cores),
			Load1:           r.Float64() * 4,
			Load5:           r.Float64() * 3,
			Load15:          r.Float64() * 2,
			MemoryTotal:     32 << 30,
			MemoryUsed:      12 << 30,
			MemoryAvailable: 20 << 30,
			SwapTotal:       8 << 30,
			SwapUsed:        1 << 30,
			Net: agentproto.NetSample{
				RxBPS:   r.Float64() * 2e6,
				TxBPS:   r.Float64() * 1e6,
				RxBytes: ts.UnixNano() % 1e15,
				TxBytes: ts.UnixNano() % 1e15,
			},
			Disks: []agentproto.DiskSample{{
				Fs: "ext4", Mount: "/",
				TotalBytes:  900 << 30,
				UsedBytes:   300 << 30,
				InodesTotal: 58_000_000,
				InodesUsed:  1_200_000,
			}},
			IO: agentproto.DiskIOSample{
				ReadBPS:  r.Float64() * 5e6, WriteBPS: r.Float64() * 3e6,
				ReadIOPS: r.Float64() * 400, WriteIOPS: r.Float64() * 250,
				TotalRead: ts.UnixNano() % 1e15, TotalWrite: ts.UnixNano() % 1e15,
			},
			TCPEstablished: 40 + r.Intn(400),
			TCPTotal:       120 + r.Intn(900),
			Processes:      250 + r.Intn(300),
			UptimeS:        int64(90 * 24 * time.Hour / time.Second),
			CollectMS:      2 + r.Int63n(9),
		},
	}
	for i := 0; i < sites; i++ {
		sampleID := fmt.Sprintf("00000000-0000-4000-8000-%012d", nodeIdx*1000+i)
		s.Sites = append(s.Sites, agentproto.SiteSample{
			WebsiteID:   sampleID,
			UnixUser:    fmt.Sprintf("ep-site%d-%d", nodeIdx, i),
			CPUPercent:  clamp(r.Float64()*15, 0, 100),
			MemoryBytes: int64(64+r.Intn(512)) << 20,
			DiskUsedMB:  int64(1024 + r.Intn(8192)),
			Processes:   5 + r.Intn(40),
		})
	}
	return s
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
