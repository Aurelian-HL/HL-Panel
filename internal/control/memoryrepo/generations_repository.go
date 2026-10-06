package memoryrepo

import (
	"context"
	"crypto/subtle"
	"fmt"
	"sort"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/nodes"
)

func (s *Store) CreateGroupRevision(_ context.Context, input generations.CreateGroupRevisionInput, event audit.Event) (generations.CreateGroupRevisionResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	group, exists := s.deviceGroups[input.GroupID]
	if !exists {
		return generations.CreateGroupRevisionResult{}, faults.ErrNotFound
	}
	if revisionID, replay := s.revisionByIdempotency[input.GroupID][input.IdempotencyKey]; replay {
		existing := s.groupRevisionByIDLocked(input.GroupID, revisionID)
		if existing.RequestSHA256 != input.RequestSHA256 {
			return generations.CreateGroupRevisionResult{}, faults.ErrIdempotencyConflict
		}
		return generations.CreateGroupRevisionResult{
			Generation:  cloneGroupRevision(existing),
			Assignments: cloneNodeConfigs(s.nodeConfigsByRevision[existing.ID]),
			Replayed:    true,
		}, nil
	}

	group.CurrentRevision++
	group.UpdatedAt = input.CreatedAt
	s.deviceGroups[group.ID] = group
	revision := generations.GroupRevision{
		ID:             input.ID,
		GroupID:        input.GroupID,
		Revision:       group.CurrentRevision,
		Engine:         input.Engine,
		Config:         cloneRaw(input.Config),
		ConfigSHA256:   input.ConfigSHA256,
		RequestSHA256:  input.RequestSHA256,
		IdempotencyKey: input.IdempotencyKey,
		CreatedBy:      input.CreatedBy,
		CreatedAt:      input.CreatedAt,
	}
	s.revisionsByGroup[input.GroupID] = append(s.revisionsByGroup[input.GroupID], revision)
	s.revisionByIdempotency[input.GroupID][input.IdempotencyKey] = revision.ID

	nodeIDs := make([]string, 0, len(s.membersByGroup[input.GroupID]))
	for nodeID, member := range s.membersByGroup[input.GroupID] {
		if member.RetiredAt == nil {
			nodeIDs = append(nodeIDs, nodeID)
		}
	}
	sort.Strings(nodeIDs)
	assignments := make([]generations.NodeConfigGeneration, 0, len(nodeIDs))
	for _, nodeID := range nodeIDs {
		assignment, created, err := s.compileNodeConfigLocked(nodeID, input.CreatedAt)
		if err != nil {
			return generations.CreateGroupRevisionResult{}, err
		}
		if created {
			assignments = append(assignments, assignment)
		}
	}
	s.nodeConfigsByRevision[revision.ID] = cloneNodeConfigs(assignments)
	s.appendAuditLocked(event)
	return generations.CreateGroupRevisionResult{
		Generation:  cloneGroupRevision(revision),
		Assignments: cloneNodeConfigs(assignments),
	}, nil
}

func (s *Store) DesiredNodeConfig(_ context.Context, nodeID string) (generations.NodeConfigGeneration, int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	node, exists := s.nodes[nodeID]
	if !exists {
		return generations.NodeConfigGeneration{}, 0, faults.ErrNotFound
	}
	if node.DesiredGeneration == 0 || node.AppliedGeneration >= node.DesiredGeneration {
		return generations.NodeConfigGeneration{}, node.AppliedGeneration, faults.ErrNotFound
	}
	configuration, exists := s.nodeConfigsByNode[nodeID][node.DesiredGeneration]
	if !exists {
		return generations.NodeConfigGeneration{}, node.AppliedGeneration, faults.ErrNotFound
	}
	return cloneNodeConfig(configuration), node.AppliedGeneration, nil
}

func (s *Store) RecordApplyResult(_ context.Context, result generations.ApplyResult, event audit.Event) (nodes.Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	node, exists := s.nodes[result.NodeID]
	if !exists {
		return nodes.Node{}, faults.ErrNotFound
	}
	configuration, exists := s.nodeConfigsByNode[result.NodeID][result.Generation]
	if !exists {
		return nodes.Node{}, fmt.Errorf("%w: generation was not assigned to node", faults.ErrConflict)
	}
	if subtle.ConstantTimeCompare([]byte(configuration.ConfigSHA256), []byte(result.ConfigSHA256)) != 1 {
		return nodes.Node{}, fmt.Errorf("%w: config hash does not match assigned generation", faults.ErrConflict)
	}
	if _, exists := s.applyResultsByNode[result.NodeID]; !exists {
		s.applyResultsByNode[result.NodeID] = make(map[int64]map[string]generations.ApplyResult)
	}
	if _, exists := s.applyAttemptsByNode[result.NodeID]; !exists {
		s.applyAttemptsByNode[result.NodeID] = make(map[int64]generations.ApplyAttemptState)
	}
	attempt := s.applyAttemptsByNode[result.NodeID][result.Generation]
	newAttempt := result.AttemptID != "" && attempt.CurrentID != result.AttemptID
	if newAttempt && attempt.SeenIDs[result.AttemptID] {
		return nodes.Node{}, fmt.Errorf("%w: stale apply attempt", faults.ErrConflict)
	}
	if result.AttemptID == "" && attempt.CurrentID != "" {
		return nodes.Node{}, fmt.Errorf("%w: legacy apply result cannot replace current attempt", faults.ErrConflict)
	}
	phaseResults := s.applyResultsByNode[result.NodeID][result.Generation]
	if phaseResults == nil || newAttempt {
		phaseResults = make(map[string]generations.ApplyResult)
	}
	phaseKey := string(result.Phase)
	if existing, exists := phaseResults[phaseKey]; exists {
		if existing.AttemptID != result.AttemptID || existing.Status != result.Status || existing.ConfigSHA256 != result.ConfigSHA256 ||
			existing.EngineMode != result.EngineMode || existing.Message != result.Message {
			return nodes.Node{}, fmt.Errorf("%w: apply phase already recorded with different content", faults.ErrConflict)
		}
		return cloneNode(node), nil
	}
	if result.AttemptID == "" && result.Phase == "verify" && result.Status == "succeeded" {
		commit, committed := phaseResults["commit"]
		if !committed || commit.Status != "succeeded" {
			return nodes.Node{}, fmt.Errorf("%w: successful commit is required before verification", faults.ErrConflict)
		}
	}
	if newAttempt {
		if attempt.SeenIDs == nil {
			attempt.SeenIDs = make(map[string]bool)
		}
		attempt.CurrentID = result.AttemptID
		attempt.Invalidated = false
		attempt.SeenIDs[result.AttemptID] = true
	}
	if result.AttemptID != "" && (result.Status != "succeeded" || result.Phase == "rollback") {
		attempt.Invalidated = true
	}
	if result.AttemptID != "" {
		s.applyAttemptsByNode[result.NodeID][result.Generation] = attempt
	}
	s.applyResultsByNode[result.NodeID][result.Generation] = phaseResults
	phaseResults[phaseKey] = result
	if result.Generation >= node.LastApplyGeneration {
		node.LastApplyGeneration = result.Generation
		node.LastApplyStatus = string(result.Status)
	}
	commit, committed := phaseResults["commit"]
	verify, verified := phaseResults["verify"]
	if !attempt.Invalidated && committed && verified && commit.Status == "succeeded" && verify.Status == "succeeded" &&
		commit.AttemptID == verify.AttemptID && commit.ConfigSHA256 == verify.ConfigSHA256 &&
		result.Generation > node.AppliedGeneration {
		node.AppliedGeneration = result.Generation
	}
	node.UpdatedAt = result.CreatedAt
	s.nodes[node.ID] = node
	s.appendAuditLocked(event)
	return cloneNode(node), nil
}

func (s *Store) groupRevisionByIDLocked(groupID, revisionID string) generations.GroupRevision {
	for _, revision := range s.revisionsByGroup[groupID] {
		if revision.ID == revisionID {
			return revision
		}
	}
	return generations.GroupRevision{}
}
