package postgressnapshot

import (
	"context"
	"database/sql"
	"errors"
	"sync"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/control/panelquota"
)

// At most two private snapshot builds per store: a slow rule computation leaves
// room for a profile save, without letting a burst allocate unbounded full
// snapshots. Waiting here holds neither a database connection nor a commit lock.
type snapshotPreparationBudget struct {
	once  sync.Once
	slots chan struct{}
}

func (b *snapshotPreparationBudget) acquire(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.once.Do(func() { b.slots = make(chan struct{}, 2) })
	select {
	case b.slots <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-b.slots
			return nil, err
		}
		return func() { <-b.slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type preparedTransaction[T any] struct {
	result      T
	base        snapshotReadCache
	cache       snapshotReadCache
	restored    *memoryrepo.Store
	events      []audit.Event
	history     []preparedNodeConfigHistory
	before      map[string]int64
	after       map[string]int64
	beforeUsage panelquota.Usage
	afterUsage  panelquota.Usage
	traffic     int64
	health      int64
}

// Opt in only operations whose effects are confined to their private memory
// store. No network calls, external writes or externally visible credentials
// may escape a preparation. A stale candidate can be recomputed, but a failed
// COMMIT is never retried here. Arbitrary callbacks retain transact's existing
// single-execution semantics.
func transactPrepared[T any](parent context.Context, s *Store, operation func(*memoryrepo.Store) (T, error)) (T, error) {
	var zero T
	ctx, cancel := context.WithTimeout(parent, operationTimeout)
	defer cancel()
	for range 2 {
		prepared, err := prepareTransaction(ctx, s, operation)
		if err != nil {
			return prepared.result, err
		}
		committed, err := commitPreparedTransaction(ctx, s, prepared)
		if err != nil {
			return zero, err
		}
		if committed {
			return prepared.result, nil
		}
	}
	// Bound wasted CPU under sustained writes. The original serialized path
	// remains a fallback, within the same overall request deadline.
	return transact(ctx, s, true, operation)
}

func prepareTransaction[T any](ctx context.Context, s *Store, operation func(*memoryrepo.Store) (T, error)) (preparedTransaction[T], error) {
	var prepared preparedTransaction[T]
	release, err := s.prepareBudget.acquire(ctx)
	if err != nil {
		return prepared, snapshotFailure(ctx, "prepare_queue", err)
	}
	defer release()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return prepared, snapshotFailure(ctx, "begin", err)
	}
	defer tx.Rollback()
	base, state, err := s.validatedReadSnapshot(ctx, tx)
	if err != nil {
		return prepared, err
	}
	if state == nil {
		state = s.takePreparedState(base)
		if state == nil {
			state, err = decodeReadSnapshot(base.payload, base.version, uint64(base.revision))
			if err != nil {
				return prepared, err
			}
		}
	}
	prepared.base = base
	prepared.traffic, prepared.health, err = readProjectionRevisions(ctx, tx)
	if err != nil {
		return prepared, err
	}
	traffic, err := s.ruleTrafficOverlaysAtRevision(ctx, tx, prepared.traffic)
	if err != nil {
		return prepared, err
	}
	health, err := s.protocolHealthOverlaysAtRevision(ctx, tx, prepared.health)
	if err != nil {
		return prepared, err
	}
	state.MergeRuleTrafficOverlays(traffic)
	state.MergeProtocolHealthOverlays(health)
	s.applyHeartbeatTelemetry(state)
	attachNodeConfigHistoryReader(ctx, tx, state)
	state.SetPanelQuotaProvider(s.quotaProvider())
	prepared.before = state.DesiredGenerationIndex()
	prepared.beforeUsage, _ = state.PanelResourceUsage(ctx)
	auditCount := len(state.AuditEvents())
	prepared.result, err = operation(state)
	if err != nil {
		return prepared, snapshotOperationFailure(ctx, err)
	}
	prepared.afterUsage, _ = state.PanelResourceUsage(ctx)
	if err := s.validatePanelGrowth(prepared.beforeUsage, prepared.afterUsage); err != nil {
		return prepared, err
	}
	events := state.AuditEvents()
	if auditCount > len(events) {
		return prepared, snapshotFailure(ctx, "audit_validate", errors.New("audit state changed unexpectedly"))
	}
	prepared.events = events[auditCount:]
	prepared.history, err = prepareNodeConfigHistory(state.DetachHistoricalNodeConfigs())
	if err != nil {
		return prepared, snapshotFailure(ctx, "history_write", err)
	}
	prepared.cache.payload, prepared.restored, err = encodePreparedHotSnapshot(state)
	if err != nil {
		return prepared, err
	}
	prepared.cache.version = memoryrepo.SnapshotVersion
	// All detached projections are built before the commit lock. Neither the
	// mutable callback store nor its transaction-bound readers are published.
	prepared.cache.identities = prepared.restored.IdentityReadView()
	prepared.cache.usage = prepared.restored.UsageReadView()
	prepared.cache.forwarding = prepared.restored.ForwardingReadView()
	prepared.after = prepared.restored.DesiredGenerationIndex()
	// Release the read-only connection before borrowing the commit connection;
	// a pool with a single connection must work without deadlocking itself.
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		return prepared, snapshotFailure(ctx, "read_rollback", err)
	}
	return prepared, nil
}

func (s *Store) takePreparedState(base snapshotReadCache) *memoryrepo.Store {
	release, ok := s.writeGate.tryAcquire()
	if !ok {
		return nil
	}
	defer release()
	prepared := s.preparedWrite
	if prepared.version != base.version || prepared.revision != base.revision || prepared.transactionID != base.transactionID {
		return nil
	}
	s.preparedWrite = preparedSnapshotWrite{}
	return prepared.state
}

// false, nil means the candidate is stale and no business write was attempted.
// Every error, especially a lost COMMIT acknowledgement, is returned unchanged
// to the caller rather than being interpreted as permission to replay.
func commitPreparedTransaction[T any](ctx context.Context, s *Store, prepared preparedTransaction[T]) (bool, error) {
	release, err := s.writeGate.acquire(ctx)
	if err != nil {
		return false, snapshotFailure(ctx, "write_queue", err)
	}
	defer release()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, snapshotFailure(ctx, "begin", err)
	}
	defer tx.Rollback()
	var current snapshotReadCache
	if err := tx.QueryRowContext(ctx, `SELECT format_version,revision,xmin::text
 FROM nyvp_control_snapshots WHERE singleton=true FOR UPDATE`).Scan(&current.version, &current.revision, &current.transactionID); err != nil {
		return false, snapshotFailure(ctx, "snapshot_lock", err)
	}
	if current.version != prepared.base.version || current.revision != prepared.base.revision || current.transactionID != prepared.base.transactionID {
		return false, nil
	}
	// All projection writers acquire the snapshot lock first (traffic uses
	// FOR SHARE). Holding FOR UPDATE makes these markers stable through COMMIT.
	traffic, health, err := readProjectionRevisions(ctx, tx)
	if err != nil {
		return false, err
	}
	if traffic != prepared.traffic || health != prepared.health {
		return false, nil
	}
	if err := s.validatePanelGrowth(prepared.beforeUsage, prepared.afterUsage); err != nil {
		return false, err
	}
	if err := appendAuditJournal(ctx, tx, prepared.events); err != nil {
		return false, snapshotFailure(ctx, "audit_write", err)
	}
	if err := persistPreparedNodeConfigHistory(ctx, tx, prepared.history); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM hl_panel_protocol_health`); err != nil {
		return false, snapshotFailure(ctx, "overlay_fold", err)
	}
	cache := prepared.cache
	if err := tx.QueryRowContext(ctx, `UPDATE nyvp_control_snapshots SET payload=$1,format_version=$2,
 revision=revision+1,updated_at=clock_timestamp() WHERE singleton=true RETURNING revision,xmin::text`, cache.payload, cache.version).
		Scan(&cache.revision, &cache.transactionID); err != nil {
		return false, snapshotFailure(ctx, "snapshot_update", err)
	}
	if err := tx.Commit(); err != nil {
		return false, snapshotFailure(ctx, "commit", err)
	}
	prepared.restored.SetPersistenceRevision(uint64(cache.revision))
	s.preparedWrite = preparedSnapshotWrite{version: cache.version, revision: cache.revision, transactionID: cache.transactionID, state: prepared.restored}
	s.readMu.Lock()
	if s.readCache.revision <= cache.revision {
		s.readCache = cache
	}
	s.readMu.Unlock()
	s.notifyDesiredChanges(prepared.before, prepared.after)
	return true, nil
}

func readProjectionRevisions(ctx context.Context, tx *sql.Tx) (traffic, health int64, err error) {
	err = tx.QueryRowContext(ctx, `SELECT
 (SELECT revision FROM hl_panel_projection_revisions WHERE projection=$1),
 (SELECT revision FROM hl_panel_projection_revisions WHERE projection=$2)`, ruleTrafficProjection, healthProjection).Scan(&traffic, &health)
	return traffic, health, snapshotFailure(ctx, "overlay_revision_read", err)
}

func (s *Store) notifyDesiredChanges(before, after map[string]int64) {
	for id, generation := range after {
		if generation != before[id] {
			s.desiredChanges.Notify(id)
		}
	}
	for id := range before {
		if _, exists := after[id]; !exists {
			s.desiredChanges.Notify(id)
		}
	}
}
