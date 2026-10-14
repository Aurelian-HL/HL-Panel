package postgressnapshot

import (
	"encoding/json"
	"time"

	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/control/nodes"
)

// Only ephemeral host readings live here. Configuration and credentials remain
// authoritative in PostgreSQL; a restart falls back to the durable snapshot.
type heartbeatTelemetry struct {
	node       nodes.Node
	receivedAt time.Time
}

const heartbeatTelemetryRetention = 2 * time.Minute

func (s *Store) publishHeartbeatTelemetry(node nodes.Node) {
	s.publishTelemetry(node, false)
}

// A committed heartbeat establishes the current identity and clock epoch.
// Reset stale overlays after credential rotation or a backwards NTP correction.
func (s *Store) publishDurableHeartbeatTelemetry(node nodes.Node) {
	s.publishTelemetry(node, true)
}

func (s *Store) publishTelemetry(node nodes.Node, durable bool) {
	if node.DeletedAt != nil || node.LastHeartbeatAt == nil {
		return
	}
	// Detach JSON telemetry before publishing it to concurrent readers.
	raw, err := json.Marshal(node.Resources)
	if err != nil {
		return
	}
	var resources map[string]any
	if json.Unmarshal(raw, &resources) != nil {
		return
	}
	at := *node.LastHeartbeatAt
	node = nodes.Node{ID: node.ID, CredentialHash: node.CredentialHash, Resources: resources, LastHeartbeatAt: &at}
	now := time.Now()
	s.telemetryMu.Lock()
	defer s.telemetryMu.Unlock()
	if s.telemetry == nil {
		s.telemetry = make(map[string]heartbeatTelemetry)
	}
	for id, value := range s.telemetry {
		if now.Sub(value.receivedAt) > heartbeatTelemetryRetention {
			delete(s.telemetry, id)
		}
	}
	previous, exists := s.telemetry[node.ID]
	if exists && !durable && !node.LastHeartbeatAt.After(*previous.node.LastHeartbeatAt) {
		return
	}
	s.telemetry[node.ID] = heartbeatTelemetry{node: node, receivedAt: now}
}

func (s *Store) applyHeartbeatTelemetry(state *memoryrepo.Store) {
	now := time.Now()
	s.telemetryMu.RLock()
	values := make([]nodes.Node, 0, len(s.telemetry))
	for _, value := range s.telemetry {
		if now.Sub(value.receivedAt) <= heartbeatTelemetryRetention {
			values = append(values, value.node)
		}
	}
	s.telemetryMu.RUnlock()
	state.MergeHeartbeatTelemetry(values)
}

func (s *Store) mergeNodeTelemetry(items []nodes.Node) {
	now := time.Now()
	s.telemetryMu.RLock()
	defer s.telemetryMu.RUnlock()
	for i := range items {
		node := &items[i]
		value, exists := s.telemetry[node.ID]
		if !exists || now.Sub(value.receivedAt) > heartbeatTelemetryRetention || node.DeletedAt != nil || node.CredentialHash != value.node.CredentialHash {
			continue
		}
		if node.LastHeartbeatAt != nil && !value.node.LastHeartbeatAt.After(*node.LastHeartbeatAt) {
			continue
		}
		raw, err := json.Marshal(value.node.Resources)
		if err != nil {
			continue
		}
		var resources map[string]any
		if json.Unmarshal(raw, &resources) != nil {
			continue
		}
		at := *value.node.LastHeartbeatAt
		node.Resources, node.LastHeartbeatAt = resources, &at
		if at.After(node.UpdatedAt) {
			node.UpdatedAt = at
		}
	}
}
