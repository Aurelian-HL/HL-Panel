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
		items = append(items, cloneNode(node))
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
		if node.NezhaServerID != 0 {
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
	s.mu.Lock()
	defer s.mu.Unlock()
	node, exists := s.nodes[nodeID]
	if !exists {
		return nodes.Node{}, faults.ErrNotFound
	}
	if heartbeat.CurrentAppliedGeneration > node.DesiredGeneration {
		return nodes.Node{}, fmt.Errorf("%w: applied generation exceeds desired generation", faults.ErrConflict)
	}
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
		node.LastApplyStatus = heartbeat.LastApplyStatus
	}
	s.nodes[nodeID] = node
	s.appendAuditLocked(event)
	return cloneNode(node), nil
}

func (s *Store) Overview(_ context.Context, now time.Time, onlineFor time.Duration) (nodes.Overview, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	overview := nodes.Overview{NodeCount: len(s.nodes), GroupCount: len(s.deviceGroups)}
	for _, node := range s.nodes {
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
