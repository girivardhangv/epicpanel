package servers

import (
	"sync"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/agentproto"
)

// Outbound registry: the persistent agent stream is bidirectional. The panel
// pushes console backfill requests and validated console commands DOWN the
// same socket the agent reports on, so no inbound management API is exposed on
// the node (spec §31–§32). One buffered channel per connected agent; a slow or
// absent agent never blocks the caller (SendToAgent is non-blocking).

var (
	agentOutMu sync.Mutex
	agentOut   = map[uuid.UUID]chan agentproto.Frame{}
)

func registerAgentConn(serverID uuid.UUID) chan agentproto.Frame {
	ch := make(chan agentproto.Frame, 128)
	agentOutMu.Lock()
	agentOut[serverID] = ch
	agentOutMu.Unlock()
	return ch
}

func unregisterAgentConn(serverID uuid.UUID) {
	agentOutMu.Lock()
	delete(agentOut, serverID)
	agentOutMu.Unlock()
}

// SendToAgent enqueues a frame for one connected agent. Returns false when the
// agent is not connected or its outbound queue is full.
func SendToAgent(serverID uuid.UUID, frame agentproto.Frame) bool {
	agentOutMu.Lock()
	ch := agentOut[serverID]
	agentOutMu.Unlock()
	if ch == nil {
		return false
	}
	select {
	case ch <- frame:
		return true
	default:
		return false
	}
}

// AgentConnected reports whether an agent stream is currently registered.
func AgentConnected(serverID uuid.UUID) bool {
	agentOutMu.Lock()
	_, ok := agentOut[serverID]
	agentOutMu.Unlock()
	return ok
}
