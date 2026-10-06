// Package memory provides a volatile usage ledger for local evaluation and
// tests. Production deployments use the normalized PostgreSQL repository.
package memory

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"

	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/usage"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

type cursorKey struct {
	nodeID string
	bootID string
}

type eventKey struct {
	nodeID   string
	bootID   string
	sequence int64
}

type resultKey struct {
	decisionID string
	revision   int64
}

type revokeMetadata struct {
	idempotencyKey string
	requestSHA256  string
}

// Repository intentionally keeps no durable state. It mirrors the same
// ordering and idempotency rules as the PostgreSQL implementation.
type Repository struct {
	mu             sync.Mutex
	cursors        map[cursorKey]int64
	events         map[eventKey]usage.Event
	eventOrder     []eventKey
	eventDecisions map[eventKey]string
	totals         map[string]usage.CustomerTotals
	decisions      map[string]usage.EnforcementDecision
	results        map[resultKey]string
	revokes        map[string]revokeMetadata
}

func New() *Repository {
	return &Repository{
		cursors:        make(map[cursorKey]int64),
		events:         make(map[eventKey]usage.Event),
		eventDecisions: make(map[eventKey]string),
		totals:         make(map[string]usage.CustomerTotals),
		decisions:      make(map[string]usage.EnforcementDecision),
		results:        make(map[resultKey]string),
		revokes:        make(map[string]revokeMetadata),
	}
}

func (repository *Repository) Ingest(_ context.Context, event usage.Event, project usage.DecisionProjector) (usage.IngestResult, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()

	key := eventKey{nodeID: event.NodeID, bootID: event.BootID, sequence: event.Sequence}
	if stored, ok := repository.events[key]; ok {
		if stored.PayloadSHA256 != event.PayloadSHA256 {
			return usage.IngestResult{}, usage.ErrIdempotencyConflict
		}
		return usage.IngestResult{
			Event: stored, CustomerTotals: repository.totals[stored.CustomerID],
			Decision: repository.decisionForEvent(key), Replayed: true,
		}, nil
	}
	cursor := cursorKey{nodeID: event.NodeID, bootID: event.BootID}
	last := repository.cursors[cursor]
	if last == math.MaxInt64 {
		return usage.IngestResult{}, usage.ErrSequenceOverflow
	}
	expected := last + 1
	if event.Sequence < expected {
		return usage.IngestResult{}, fmt.Errorf("%w: expected %d, received %d", usage.ErrSequenceOutOfOrder, expected, event.Sequence)
	}
	if event.Sequence > expected {
		return usage.IngestResult{}, fmt.Errorf("%w: expected %d, received %d", usage.ErrSequenceGap, expected, event.Sequence)
	}

	totals := repository.totals[event.CustomerID]
	if event.CustomerActualBytes > math.MaxInt64-totals.ActualBytes || event.ChargedBytes > math.MaxInt64-totals.ChargedBytes {
		return usage.IngestResult{}, usage.ErrByteOverflow
	}
	totals.CustomerID = event.CustomerID
	totals.ActualBytes += event.CustomerActualBytes
	totals.ChargedBytes += event.ChargedBytes
	if event.OccurredAt.After(totals.LastUsageOccurredAt) {
		totals.LastUsageOccurredAt = event.OccurredAt
	}
	totals.UpdatedAt = event.ReceivedAt

	var decision *usage.EnforcementDecision
	var err error
	if project != nil {
		decision, err = project(totals)
		if err != nil {
			return usage.IngestResult{}, err
		}
	}
	if decision != nil {
		if decision.Status != usage.EnforcementPending || decision.CustomerID != event.CustomerID ||
			decision.TriggerNodeID != event.NodeID || decision.TriggerBootID != event.BootID ||
			decision.TriggerSequence != event.Sequence {
			return usage.IngestResult{}, fmt.Errorf("invalid usage enforcement projection")
		}
		if _, exists := repository.decisions[decision.ID]; exists {
			return usage.IngestResult{}, faults.ErrConflict
		}
	}

	repository.events[key] = event
	repository.eventOrder = append(repository.eventOrder, key)
	repository.cursors[cursor] = event.Sequence
	repository.totals[event.CustomerID] = totals
	if decision != nil {
		repository.decisions[decision.ID] = *decision
		repository.eventDecisions[key] = decision.ID
		copy := *decision
		decision = &copy
	}
	return usage.IngestResult{Event: event, CustomerTotals: totals, Decision: decision}, nil
}

func (repository *Repository) ReplayLegacy(_ context.Context, report usage.Report) (usage.IngestResult, bool, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	key := eventKey{nodeID: strings.TrimSpace(report.NodeID), bootID: strings.TrimSpace(report.BootID), sequence: report.Sequence}
	stored, exists := repository.events[key]
	if !exists {
		return usage.IngestResult{}, false, nil
	}
	if !usage.LegacyReplayMatches(stored, report) {
		return usage.IngestResult{}, false, usage.ErrIdempotencyConflict
	}
	return usage.IngestResult{
		Event: stored, CustomerTotals: repository.totals[stored.CustomerID],
		Decision: repository.decisionForEvent(key), Replayed: true,
	}, true, nil
}

func (repository *Repository) Query(_ context.Context, query usage.Query) (usage.QueryResult, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()

	items := make([]usage.Record, 0, len(repository.eventOrder))
	var totals usage.Totals
	for _, key := range repository.eventOrder {
		event := repository.events[key]
		if !matches(query, event) {
			continue
		}
		totals.RuleActualBytes += event.RuleActualBytes
		totals.CustomerActualBytes += event.CustomerActualBytes
		totals.ChargedBytes += event.ChargedBytes
		items = append(items, usage.Record{Event: event, Decision: repository.decisionForEvent(key)})
	}
	sort.Slice(items, func(i, j int) bool {
		left, right := items[i].Event, items[j].Event
		if !left.OccurredAt.Equal(right.OccurredAt) {
			return left.OccurredAt.After(right.OccurredAt)
		}
		if left.NodeID != right.NodeID {
			return left.NodeID < right.NodeID
		}
		if left.BootID != right.BootID {
			return left.BootID < right.BootID
		}
		return left.Sequence > right.Sequence
	})
	total := int64(len(items))
	start := (query.Page - 1) * query.PageSize
	if start >= len(items) {
		items = []usage.Record{}
	} else {
		end := min(start+query.PageSize, len(items))
		items = items[start:end]
	}
	return usage.QueryResult{Items: items, Totals: totals, Total: total, Page: query.Page, PageSize: query.PageSize}, nil
}

func (repository *Repository) DesiredEnforcement(_ context.Context, nodeID string) (*usage.EnforcementDecision, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	var selected *usage.EnforcementDecision
	for _, item := range repository.decisions {
		if item.TriggerNodeID != nodeID || (item.Status != usage.EnforcementPending && item.Status != usage.EnforcementRevokePending) {
			continue
		}
		if selected == nil || item.CreatedAt.Before(selected.CreatedAt) || (item.CreatedAt.Equal(selected.CreatedAt) && item.ID < selected.ID) {
			copy := item
			selected = &copy
		}
	}
	return selected, nil
}

func (repository *Repository) RecordEnforcementResult(_ context.Context, result usage.EnforcementResult) (usage.EnforcementDecision, bool, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	key := resultKey{decisionID: result.DecisionID, revision: result.Revision}
	if payload, exists := repository.results[key]; exists {
		if payload != result.PayloadSHA256 {
			return usage.EnforcementDecision{}, false, usage.ErrIdempotencyConflict
		}
		decision, ok := repository.decisions[result.DecisionID]
		if !ok {
			return usage.EnforcementDecision{}, false, faults.ErrNotFound
		}
		return decision, true, nil
	}
	decision, ok := repository.decisions[result.DecisionID]
	if !ok {
		return usage.EnforcementDecision{}, false, faults.ErrNotFound
	}
	if decision.TriggerNodeID != result.NodeID || decision.Revision != result.Revision || decision.Action != result.Action {
		return usage.EnforcementDecision{}, false, fmt.Errorf("%w: enforcement command is stale or belongs to another node", faults.ErrConflict)
	}
	if decision.Status != usage.EnforcementPending && decision.Status != usage.EnforcementRevokePending {
		return usage.EnforcementDecision{}, false, fmt.Errorf("%w: enforcement command is not pending", faults.ErrConflict)
	}
	if decision.Revision == math.MaxInt64 {
		return usage.EnforcementDecision{}, false, usage.ErrSequenceOverflow
	}
	repository.results[key] = result.PayloadSHA256
	if result.Status == agentv1.EnforcementResultSucceeded {
		if decision.Status == usage.EnforcementPending {
			decision.Status = usage.EnforcementApplied
		} else {
			decision.Status = usage.EnforcementRevoked
		}
		decision.LastError = ""
	} else {
		if decision.Status == usage.EnforcementPending {
			decision.Status = usage.EnforcementApplyFailed
		} else {
			decision.Status = usage.EnforcementRevokeFailed
		}
		decision.LastError = result.Message
	}
	decision.Revision++
	decision.UpdatedAt = result.CreatedAt
	repository.decisions[decision.ID] = decision
	return decision, false, nil
}

func (repository *Repository) RequestRevoke(_ context.Context, request usage.RevokeRequest) (usage.EnforcementDecision, bool, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	decision, ok := repository.decisions[request.DecisionID]
	if !ok {
		return usage.EnforcementDecision{}, false, faults.ErrNotFound
	}
	if existing, exists := repository.revokes[request.DecisionID]; exists {
		if existing.idempotencyKey != request.IdempotencyKey || existing.requestSHA256 != request.RequestSHA256 {
			return usage.EnforcementDecision{}, false, usage.ErrIdempotencyConflict
		}
		return decision, true, nil
	}
	if decision.Status == usage.EnforcementRevoked {
		return usage.EnforcementDecision{}, false, fmt.Errorf("%w: enforcement decision is already revoked", faults.ErrConflict)
	}
	if decision.Revision == math.MaxInt64 {
		return usage.EnforcementDecision{}, false, usage.ErrSequenceOverflow
	}
	decision.Action = agentv1.EnforcementEnableCustomer
	decision.Status = usage.EnforcementRevokePending
	decision.LastError = ""
	decision.Revision++
	decision.UpdatedAt = request.RequestedAt
	repository.decisions[decision.ID] = decision
	repository.revokes[decision.ID] = revokeMetadata{idempotencyKey: request.IdempotencyKey, requestSHA256: request.RequestSHA256}
	return decision, false, nil
}

func (repository *Repository) decisionForEvent(key eventKey) *usage.EnforcementDecision {
	id := repository.eventDecisions[key]
	if id == "" {
		return nil
	}
	decision, ok := repository.decisions[id]
	if !ok {
		return nil
	}
	copy := decision
	return &copy
}

func matches(query usage.Query, event usage.Event) bool {
	switch query.Scope {
	case usage.ScopeCustomer:
		if event.CustomerID != query.ScopeID {
			return false
		}
	case usage.ScopeRule:
		if event.RuleID != query.ScopeID {
			return false
		}
	case usage.ScopeDeviceGroup:
		if event.EntryGroupID != query.ScopeID && event.ExitGroupID != query.ScopeID {
			return false
		}
	}
	if query.From != nil && event.OccurredAt.Before(*query.From) {
		return false
	}
	if query.To != nil && !event.OccurredAt.Before(*query.To) {
		return false
	}
	return true
}

var _ usage.Repository = (*Repository)(nil)
