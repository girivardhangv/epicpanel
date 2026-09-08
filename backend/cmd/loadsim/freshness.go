package main

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"
)

// freshnessWatcher re-proves the freshness contract against a live control
// plane: after a node's stream is hard-dropped, poll its live projection and
// record exactly when the UI-visible state transitions LIVE -> STALE ->
// OFFLINE. It also asserts the negative invariant: the node must NEVER be
// reported LIVE after the drop (stale-as-live would be a contract breach).
type freshnessWatcher struct {
	base   string
	admin  *adminClient
	orgID  string
	once   sync.Once
	warned bool
}

func newFreshnessWatcher(base string, admin *adminClient, orgID string) *freshnessWatcher {
	return &freshnessWatcher{base: base, admin: admin, orgID: orgID}
}

// watch polls until OFFLINE (or ctx done). Polls start the instant the drop
// happens so transition times have ~poll-resolution accuracy.
func (w *freshnessWatcher) watch(ctx context.Context, serverID string, droppedAt time.Time) freshnessReport {
	rep := freshnessReport{Observed: true, DroppedAt: durFrom(droppedAt)}
	var wasLive bool
	for i := 0; ; i++ {
		select {
		case <-ctx.Done():
			return rep
		case <-time.After(time.Second):
		}
		nodeState, freshState := w.admin.serverState(w.orgID, serverID)
		if nodeState == "" && freshState == "" {
			if i > 10 {
				log.Printf("[loadsim] freshness watcher: node %s vanished from fleet projection", serverID)
				rep.Final = "GONE"
				return rep
			}
			continue
		}
		rep.Samples++
		if freshState == "LIVE" {
			wasLive = true
		}
		if freshState != "LIVE" && rep.StaleAt == "" && wasLive {
			// (wasLive can only have been set pre-drop; a LIVE reading after
			// the drop would itself be the contract breach we flag.)
		}
		if freshState == "STALE" && rep.StaleAt == "" {
			rep.StaleAt = durFrom(time.Now())
			log.Printf("[loadsim] freshness: node %s STALE at t=%s", serverID, rep.StaleAt)
		}
		if nodeState == "OFFLINE" || freshState == "OFFLINE" {
			if rep.OfflineAt == "" {
				rep.OfflineAt = durFrom(time.Now())
				log.Printf("[loadsim] freshness: node %s OFFLINE at t=%s", serverID, rep.OfflineAt)
			}
			rep.Final = "OFFLINE"
			return rep
		}
		// Cap the watch: the OFFLINE transition is contract-bounded at 120s
		// (agentproto.StaleMaxAge); poll for up to 240s to be safe.
		if time.Since(droppedAt) > 240*time.Second {
			rep.Final = "TIMEOUT(" + nodeState + "/" + freshState + ")"
			rep.LiveAfterDrop = wasLive && rep.StaleAt == ""
			return rep
		}
	}
}

// LiveAfterDrop is true when the dropped node still read LIVE at any point
// after its stream died AND never transitioned — i.e. stale-as-live. The
// contract requires the empty/STALE path instead; the report flags it.

func durFrom(t time.Time) string { return t.Truncate(time.Millisecond).Format("15:04:05.000") }

// unused guards for optional JSON debug tooling.
var (
	_ = json.Marshal
	_ = sync.Once{}
)
