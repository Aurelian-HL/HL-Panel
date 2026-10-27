package memoryrepo

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/nodes"
)

func nodeCredentialRotationKey(input nodes.RotateCredentialInput) string {
	return input.UpdatedBy + "\x00node.credential.rotate\x00" + input.NodeID + "\x00" + input.IdempotencyKey
}

func (s *Store) NodeByCredentialHash(_ context.Context, credentialHash string) (nodes.Node, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	nodeID, exists := s.nodesByCredential[credentialHash]
	if !exists {
		return nodes.Node{}, faults.ErrUnauthorized
	}
	return cloneNode(s.nodes[nodeID]), nil
}

func (s *Store) ListNodes(_ context.Context) ([]nodes.Node, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]nodes.Node, 0, len(s.nodes))
	for _, node := range s.nodes {
		if node.DeletedAt == nil {
			items = append(items, cloneNode(node))
		}
	}
	sort.Slice(items, func(left, right int) bool {
		if items[left].Name == items[right].Name {
			return items[left].ID < items[right].ID
		}
		return items[left].Name < items[right].Name
	})
	return items, nil
}

func (s *Store) ListNezhaBindings(_ context.Context) (map[string]uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	bindings := make(map[string]uint64)
	for id, node := range s.nodes {
		if node.DeletedAt == nil && node.NezhaServerID != 0 {
			bindings[id] = node.NezhaServerID
		}
	}
	return bindings, nil
}

func (s *Store) RotateCredential(_ context.Context, input nodes.RotateCredentialInput, event audit.Event) (nodes.CredentialRotationResult, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := nodeCredentialRotationKey(input)
	var replay nodes.CredentialRotationResult
	if replayed, err := s.replayBusinessLocked(key, input.RequestSHA256, &replay); err != nil || replayed {
		return replay, replayed, err
	}
	if input.NodeID == "" || input.CredentialHash == "" || input.UpdatedBy == "" || input.IdempotencyKey == "" || input.RequestSHA256 == "" || input.UpdatedAt.IsZero() {
		return nodes.CredentialRotationResult{}, false, fmt.Errorf("%w: invalid node credential rotation mutation", faults.ErrValidation)
	}
	node, exists := s.nodes[input.NodeID]
	if !exists {
		return nodes.CredentialRotationResult{}, false, faults.ErrNotFound
	}
	if node.DeletedAt != nil {
		return nodes.CredentialRotationResult{}, false, faults.ErrNotFound
	}
	if _, collision := s.nodesByCredential[input.CredentialHash]; collision {
		return nodes.CredentialRotationResult{}, false, faults.ErrConflict
	}
	result := nodes.CredentialRotationResult{NodeID: node.ID, RotatedAt: input.UpdatedAt.UTC()}
	if err := s.recordBusinessLocked(key, input.RequestSHA256, node.ID, result); err != nil {
		return nodes.CredentialRotationResult{}, false, err
	}
	if node.CredentialHash != "" {
		delete(s.nodesByCredential, node.CredentialHash)
	}
	node.CredentialHash = input.CredentialHash
	node.UpdatedAt = result.RotatedAt
	s.nodes[input.NodeID] = node
	s.nodesByCredential[input.CredentialHash] = node.ID
	s.appendAuditLocked(event)
	return result, false, nil
}

func (s *Store) UpdateHeartbeat(_ context.Context, nodeID string, heartbeat nodes.Heartbeat, now time.Time, event audit.Event) (nodes.Node, error) {
	return s.updateHeartbeat(nodeID, heartbeat, now, event, true)
}

// UpdateHeartbeatTelemetry is for durable repository read-only fast paths.
// It returns current liveness without publishing a new configuration generation
// that has not been persisted. Reconciliation belongs in the write transaction.
func (s *Store) UpdateHeartbeatTelemetry(_ context.Context, nodeID string, heartbeat nodes.Heartbeat, now time.Time, event audit.Event) (nodes.Node, error) {
	return s.updateHeartbeat(nodeID, heartbeat, now, event, false)
}

func (s *Store) updateHeartbeat(nodeID string, heartbeat nodes.Heartbeat, now time.Time, event audit.Event, reconcile bool) (nodes.Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	node, exists := s.nodes[nodeID]
	if !exists {
		return nodes.Node{}, faults.ErrNotFound
	}
	if node.DeletedAt != nil {
		return nodes.Node{}, faults.ErrUnauthorized
	}
	if heartbeat.CurrentAppliedGeneration > node.DesiredGeneration {
		return nodes.Node{}, fmt.Errorf("%w: applied generation exceeds desired generation", faults.ErrConflict)
	}
	previousNode := node
	node.BootID = heartbeat.BootID
	node.Hostname = heartbeat.Hostname
	node.Platform = heartbeat.Platform
	node.Architecture = heartbeat.Architecture
	node.AgentVersion = heartbeat.AgentVersion
	node.EngineVersions = cloneStringMap(heartbeat.EngineVersions)
	node.Resources = cloneAnyMap(heartbeat.Resources)
	node.Capabilities = cloneStrings(heartbeat.Capabilities)
	node.LastHeartbeatAt = timePointer(now)
	node.UpdatedAt = now
	if heartbeat.LastApplyStatus != "" {
		node.LastApplyPhase = heartbeat.LastApplyPhase
		node.LastApplyStatus = heartbeat.LastApplyStatus
		node.LastApplyMessage = heartbeat.LastApplyMessage
	} else if heartbeat.LastApplyPhase != "" || heartbeat.LastApplyMessage != "" {
		node.LastApplyPhase = heartbeat.LastApplyPhase
		node.LastApplyMessage = heartbeat.LastApplyMessage
	}
	s.nodes[nodeID] = node
	// Enrollment may predate available engine capabilities, and older panels
	// could leave new members without an initial configuration. Reconcile those
	// nodes without reissuing bundles for ordinary heartbeats on existing nodes.
	if reconcile && (nodeSupportsUsageGeneration(previousNode) != nodeSupportsUsageGeneration(node) ||
		nodeSupportsVLESSReality(previousNode) != nodeSupportsVLESSReality(node) ||
		nodeSupportsVLESSUDPDirect(previousNode) != nodeSupportsVLESSUDPDirect(node) ||
		nodeSupportsGOSTUDPDirect(previousNode) != nodeSupportsGOSTUDPDirect(node) ||
		nodeSupportsReliableUDP(previousNode) != nodeSupportsReliableUDP(node) ||
		nodeSupportsTCSpeedLimit(previousNode) != nodeSupportsTCSpeedLimit(node) ||
		node.BundleCompilerRevision < NodeBundleCompilerRevision) {
		groups := []string{}
		for groupID, members := range s.membersByGroup {
			if m, ok := members[nodeID]; ok && m.RetiredAt == nil {
				groups = append(groups, groupID)
			}
		}
		_, _, compileErr := s.recompileMemberConfigsLocked(nodeID, groups, now)
		if err := compileErr; err != nil {
			if previousNode.DesiredGeneration > 0 {
				s.nodes[nodeID] = previousNode
				return nodes.Node{}, err
			}
			// Match initial enrollment: a broken historical rule must not
			// discard liveness while the node waits for its first valid bundle.
			if event.Metadata == nil {
				event.Metadata = make(map[string]any)
			}
			event.Metadata["initial_config_pending"] = true
		}
		node = s.nodes[nodeID]
	}
	// Routine liveness telemetry is persisted separately by the PostgreSQL
	// adapter and must not grow the audit journal. Keep this conditional so
	// direct repository callers can still record an explicit event (for
	// migrations and focused tests).
	if event.ID != "" {
		s.appendAuditLocked(event)
	}
	return cloneNode(node), nil
}

func (s *Store) Overview(_ context.Context, now time.Time, onlineFor time.Duration) (nodes.Overview, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	overview := nodes.Overview{GroupCount: len(s.deviceGroups)}
	for _, node := range s.nodes {
		if node.DeletedAt != nil {
			continue
		}
		overview.NodeCount++
		if node.LastHeartbeatAt != nil && now.Sub(*node.LastHeartbeatAt) <= onlineFor {
			overview.OnlineNodeCount++
		}
		if node.DesiredGeneration > node.AppliedGeneration {
			overview.SyncingNodeCount++
		}
		if node.LastApplyStatus == "failed" {
			overview.FailedApplyCount++
		}
	}
	return overview, nil
}

func (s *Store) RequestControl(_ context.Context, input nodes.ControlCommandInput, event audit.Event) (nodes.ControlCommandResult, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	node, ok := s.nodes[input.NodeID]
	if !ok || node.DeletedAt != nil {
		return nodes.ControlCommandResult{}, false, faults.ErrNotFound
	}
	if node.ControlCommandID != "" && node.ControlIdempotencyKey == input.IdempotencyKey {
		if node.ControlRequestSHA256 != input.RequestSHA256 {
			return nodes.ControlCommandResult{}, false, faults.ErrIdempotencyConflict
		}
		return controlResult(node), true, nil
	}
	node.ControlCommandID = input.CommandID
	node.ControlCommand = input.Command
	node.ControlCommandStatus = "pending"
	node.ControlCommandMessage = "等待节点执行"
	node.ControlCommandLogs = ""
	node.ControlCommandUpdatedAt = &input.UpdatedAt
	node.ControlIdempotencyKey = input.IdempotencyKey
	node.ControlRequestSHA256 = input.RequestSHA256
	node.UpdatedAt = input.UpdatedAt
	s.nodes[input.NodeID] = node
	s.appendAuditLocked(event)
	return controlResult(node), false, nil
}

func (s *Store) ControlForNode(_ context.Context, nodeID string) (nodes.ControlCommandResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	node, ok := s.nodes[nodeID]
	if !ok || node.DeletedAt != nil {
		return nodes.ControlCommandResult{}, faults.ErrNotFound
	}
	return controlResult(node), nil
}

func (s *Store) RecordControlResult(_ context.Context, nodeID string, result nodes.ControlCommandResult, event audit.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	node, ok := s.nodes[nodeID]
	if !ok || node.DeletedAt != nil {
		return faults.ErrNotFound
	}
	if node.ControlCommandID != result.CommandID || node.ControlCommand != result.Command {
		return faults.ErrConflict
	}
	node.ControlCommandStatus = result.Status
	node.ControlCommandMessage = result.Message
	node.ControlCommandLogs = result.Logs
	node.ControlCommandUpdatedAt = &result.UpdatedAt
	node.UpdatedAt = result.UpdatedAt
	s.nodes[nodeID] = node
	s.appendAuditLocked(event)
	return nil
}

func controlResult(node nodes.Node) nodes.ControlCommandResult {
	return nodes.ControlCommandResult{NodeID: node.ID, CommandID: node.ControlCommandID, Command: node.ControlCommand, Status: node.ControlCommandStatus, Message: node.ControlCommandMessage, Logs: node.ControlCommandLogs, UpdatedAt: valueTime(node.ControlCommandUpdatedAt)}
}

func valueTime(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return value.UTC()
}
