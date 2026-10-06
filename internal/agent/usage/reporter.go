// Package usage owns durable edge-agent accounting delivery. It never invents
// counters: a concrete engine-backed CounterSource is mandatory.
package usage

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

var ErrCounterSourceUnavailable = errors.New("usage counter source is not configured")

type CollectionWindow struct {
	StartedAt time.Time
	EndedAt   time.Time
}

type CounterDelta struct {
	CustomerID            string
	RuleID                string
	LegacyRuleID          string
	EntryGroupID          string
	ExitGroupID           string
	Protocol              string
	OccurredAt            time.Time
	RuleActualBytes       int64
	CustomerActualBytes   int64
	EntryMultiplierMicros int64
	ExitMultiplierMicros  int64
}

// CounterSource must return authoritative deltas for the complete closed
// window. An unavailable engine must return an error, never an empty fixture.
type CounterSource interface {
	CollectUsage(context.Context, CollectionWindow) ([]CounterDelta, error)
}

// CheckpointedCounterSource lets the reporter make source cursor advancement
// follow the durable journal checkpoint. A source must not make a collected
// cumulative-counter baseline irreversible until CommitCollection is called.
// Older sources can continue implementing CounterSource only; they retain the
// existing behavior and are still accepted by NewReporter.
type CheckpointedCounterSource interface {
	CounterSource
	BeginCollection() error
	CommitCollection()
	RollbackCollection()
}

type Sender interface {
	ReportUsage(context.Context, string, agentv1.UsageReport) (agentv1.UsageAcknowledgement, error)
}

type Reporter struct {
	store          *JournalStore
	source         CounterSource
	sender         Sender
	nodeID         string
	nodeCredential string
	maxBatch       int
	now            func() time.Time
	mu             sync.Mutex
}

func NewReporter(store *JournalStore, source CounterSource, sender Sender, nodeID, nodeCredential string, maxBatch int, now func() time.Time) (*Reporter, error) {
	if store == nil || sender == nil {
		return nil, errors.New("usage reporter store and sender are required")
	}
	if source == nil {
		return nil, ErrCounterSourceUnavailable
	}
	if !validIdentifier(nodeID) || strings.TrimSpace(nodeCredential) == "" {
		return nil, errors.New("usage reporter node identity is invalid")
	}
	if maxBatch < 1 || maxBatch > maxPendingItems {
		return nil, fmt.Errorf("usage reporter max batch must be between 1 and %d", maxPendingItems)
	}
	if now == nil {
		now = time.Now
	}
	if _, err := store.Prepare(now().UTC()); err != nil {
		return nil, err
	}
	return &Reporter{store: store, source: source, sender: sender, nodeID: nodeID, nodeCredential: nodeCredential, maxBatch: maxBatch, now: now}, nil
}

func (reporter *Reporter) FlushOnce(ctx context.Context) error {
	reporter.mu.Lock()
	defer reporter.mu.Unlock()
	journal, err := reporter.store.Load()
	if err != nil {
		return err
	}
	if len(journal.Pending) == 0 {
		journal, err = reporter.collect(ctx, journal)
		if err != nil {
			return err
		}
	}
	for len(journal.Pending) > 0 {
		pending := journal.Pending[0]
		acknowledgement, err := reporter.sender.ReportUsage(ctx, reporter.nodeCredential, pending)
		if err != nil {
			return err
		}
		if acknowledgement.NodeID != pending.NodeID || acknowledgement.BootID != pending.BootID || acknowledgement.Sequence != pending.Sequence {
			return errors.New("usage acknowledgement does not match pending report")
		}
		journal, err = reporter.store.update(func(current *Journal) error {
			if len(current.Pending) == 0 || current.Pending[0].NodeID != pending.NodeID || current.Pending[0].BootID != pending.BootID || current.Pending[0].Sequence != pending.Sequence {
				return errors.New("usage pending journal changed while acknowledging report")
			}
			current.Pending = append([]agentv1.UsageReport(nil), current.Pending[1:]...)
			current.UpdatedAt = reporter.now().UTC()
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (reporter *Reporter) collect(ctx context.Context, journal Journal) (Journal, error) {
	endedAt := reporter.now().UTC()
	if !endedAt.After(journal.LastCollectedAt) {
		return journal, nil
	}
	window := CollectionWindow{StartedAt: journal.LastCollectedAt, EndedAt: endedAt}
	checkpointed, hasCheckpoint := reporter.source.(CheckpointedCounterSource)
	if hasCheckpoint {
		if err := checkpointed.BeginCollection(); err != nil {
			return Journal{}, fmt.Errorf("begin usage collection checkpoint: %w", err)
		}
		committed := false
		defer func() {
			if !committed {
				checkpointed.RollbackCollection()
			}
		}()
		result, err := reporter.collectWindow(ctx, journal, window, endedAt)
		if err != nil {
			return Journal{}, err
		}
		checkpointed.CommitCollection()
		committed = true
		return result, nil
	}
	return reporter.collectWindow(ctx, journal, window, endedAt)
}

func (reporter *Reporter) collectWindow(ctx context.Context, journal Journal, window CollectionWindow, endedAt time.Time) (Journal, error) {
	deltas, err := reporter.source.CollectUsage(ctx, window)
	if err != nil {
		return Journal{}, err
	}
	if len(deltas) > reporter.maxBatch {
		return Journal{}, fmt.Errorf("usage source returned %d records, maximum is %d", len(deltas), reporter.maxBatch)
	}
	if int64(len(deltas)) > math.MaxInt64-journal.NextSequence {
		return Journal{}, errors.New("usage sequence cannot be incremented")
	}
	reports := make([]agentv1.UsageReport, len(deltas))
	for index, delta := range deltas {
		report, err := buildReport(reporter.nodeID, journal.BootID, journal.NextSequence+int64(index), window, delta)
		if err != nil {
			return Journal{}, fmt.Errorf("usage record %d: %w", index, err)
		}
		reports[index] = report
	}
	return reporter.store.update(func(current *Journal) error {
		if len(current.Pending) != 0 || current.BootID != journal.BootID || current.NextSequence != journal.NextSequence || !current.LastCollectedAt.Equal(journal.LastCollectedAt) {
			return errors.New("usage journal changed during collection")
		}
		current.Pending = reports
		current.NextSequence += int64(len(reports))
		current.LastCollectedAt = endedAt
		current.UpdatedAt = endedAt
		return nil
	})
}

func buildReport(nodeID, bootID string, sequence int64, window CollectionWindow, delta CounterDelta) (agentv1.UsageReport, error) {
	delta.CustomerID = strings.TrimSpace(delta.CustomerID)
	delta.RuleID = strings.TrimSpace(delta.RuleID)
	delta.LegacyRuleID = strings.TrimSpace(delta.LegacyRuleID)
	delta.EntryGroupID = strings.TrimSpace(delta.EntryGroupID)
	delta.ExitGroupID = strings.TrimSpace(delta.ExitGroupID)
	delta.Protocol = strings.ToLower(strings.TrimSpace(delta.Protocol))
	if delta.LegacyRuleID != "" {
		if !validIdentifier(delta.LegacyRuleID) || delta.RuleID != delta.LegacyRuleID || delta.CustomerID != "" ||
			delta.EntryGroupID != "" || delta.ExitGroupID != "" || delta.Protocol != "" {
			return agentv1.UsageReport{}, errors.New("legacy counter metadata is invalid")
		}
		if delta.RuleActualBytes < 0 || delta.CustomerActualBytes < 0 || delta.RuleActualBytes != delta.CustomerActualBytes {
			return agentv1.UsageReport{}, errors.New("legacy counter bytes are invalid")
		}
		if delta.OccurredAt.IsZero() {
			delta.OccurredAt = window.EndedAt
		}
		return agentv1.UsageReport{
			NodeID: nodeID, BootID: bootID, Sequence: sequence, LegacyRuleID: delta.LegacyRuleID, RuleID: delta.RuleID,
			OccurredAt: delta.OccurredAt.UTC(), PeriodStartedAt: window.StartedAt.UTC(), PeriodEndedAt: window.EndedAt.UTC(),
			RuleActualBytes: delta.RuleActualBytes, CustomerActualBytes: delta.CustomerActualBytes,
			EntryMultiplierMicros: agentv1.UsageMultiplierScale, ExitMultiplierMicros: agentv1.UsageMultiplierScale,
		}, nil
	}
	if !validIdentifier(delta.CustomerID) || !validIdentifier(delta.RuleID) || !validIdentifier(delta.EntryGroupID) || (delta.ExitGroupID != "" && !validIdentifier(delta.ExitGroupID)) {
		return agentv1.UsageReport{}, errors.New("counter identifiers are invalid")
	}
	if delta.Protocol != "tcp" && delta.Protocol != "udp" {
		return agentv1.UsageReport{}, errors.New("counter protocol must be tcp or udp")
	}
	if delta.RuleActualBytes < 0 || delta.CustomerActualBytes < 0 {
		return agentv1.UsageReport{}, errors.New("counter bytes must be non-negative")
	}
	if delta.EntryMultiplierMicros < 1 || delta.EntryMultiplierMicros > agentv1.UsageMaxMultiplierMicros || delta.ExitMultiplierMicros < 1 || delta.ExitMultiplierMicros > agentv1.UsageMaxMultiplierMicros {
		return agentv1.UsageReport{}, errors.New("counter multiplier is invalid")
	}
	if delta.OccurredAt.IsZero() {
		delta.OccurredAt = window.EndedAt
	}
	return agentv1.UsageReport{
		NodeID: nodeID, BootID: bootID, Sequence: sequence, CustomerID: delta.CustomerID, RuleID: delta.RuleID,
		EntryGroupID: delta.EntryGroupID, ExitGroupID: delta.ExitGroupID, Protocol: delta.Protocol,
		OccurredAt:      delta.OccurredAt.UTC(),
		PeriodStartedAt: window.StartedAt.UTC(), PeriodEndedAt: window.EndedAt.UTC(), RuleActualBytes: delta.RuleActualBytes,
		CustomerActualBytes: delta.CustomerActualBytes, EntryMultiplierMicros: delta.EntryMultiplierMicros,
		ExitMultiplierMicros: delta.ExitMultiplierMicros,
	}, nil
}

func validIdentifier(value string) bool {
	return value != "" && len(value) <= 128 && strings.IndexFunc(value, func(character rune) bool {
		return unicode.IsSpace(character) || unicode.IsControl(character)
	}) < 0
}
