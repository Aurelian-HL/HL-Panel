package memoryrepo

import (
	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/customeridentity"
	"github.com/hongle/hl-panel/internal/control/gatewaymembership"
)

// PrepareMigrationRestore preserves node/config/credential identity, but never
// imports authenticated browser sessions or stale node health/control requests.
func PrepareMigrationRestore(raw []byte, event audit.Event) ([]byte, error) {
	s, err := DecodeSnapshot(raw)
	if err != nil {
		return nil, err
	}
	s.sessionsByHash = map[string]auth.Session{}
	s.customerSessions = map[string]customeridentity.Session{}
	s.protocolHealth = map[string]map[string]gatewaymembership.ProtocolObservation{}
	for id, node := range s.nodes {
		node.LastHeartbeatAt = nil
		node.Resources = map[string]any{}
		node.ControlCommand = ""
		node.ControlCommandID = ""
		node.ControlCommandStatus = ""
		node.ControlCommandLogs = ""
		node.ControlCommandMessage = ""
		node.ControlCommandUpdatedAt = nil
		node.ControlIdempotencyKey = ""
		node.ControlRequestSHA256 = ""
		s.nodes[id] = node
	}
	s.appendAuditLocked(event)
	return s.EncodeSnapshot()
}
