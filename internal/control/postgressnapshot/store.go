// Package postgressnapshot provides bounded PostgreSQL-backed transactional
// snapshots for a single-instance evaluation deployment. It is not the planned
// normalized relational repository and is not a high-throughput data store.
package postgressnapshot

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/gatewaymembership"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/control/panelquota"
	_ "github.com/jackc/pgx/v5/stdlib"
	"golang.org/x/sync/singleflight"
)

const operationTimeout = 10 * time.Second

// Heartbeat resources change on every sample and do not need a full snapshot
// rewrite at the agent heartbeat cadence. Stable lifecycle changes still write
// immediately; routine telemetry is persisted at least twice per online TTL.
const heartbeatSnapshotInterval = 30 * time.Second

// PoolConfig controls the bounded connection pool used by the snapshot store.
// The variadic argument on Open keeps existing callers source-compatible while
// allowing the control API to size the pool for concurrent probes and heartbeats.
type PoolConfig struct {
	MaxOpenConns int
	MaxIdleConns int
}

const (
	defaultMaxOpenConns = 16
	defaultMaxIdleConns = 8
)

// Writes always load private state inside a row-locked database transaction.
// Reads reuse a validated snapshot only while its database row identity matches.
// Database errors are intentionally redacted to avoid leaking DSNs or payloads.
type Store struct {
	quotaMu        sync.RWMutex
	panelQuota     panelquota.Provider
	db             *sql.DB
	desiredChanges generations.ChangeSignals
	readMu         sync.Mutex
	readCache      snapshotReadCache
	readBuild      singleflight.Group
	heartbeatMu    sync.Mutex
	heartbeats     map[string]heartbeatPersistence
	telemetryMu    sync.RWMutex
	telemetry      map[string]heartbeatTelemetry
	projectionMu   sync.RWMutex
	projections    projectionCache
}

type projectionCache struct {
	ruleTrafficRevision int64
	ruleTraffic         []memoryrepo.RuleTrafficOverlay
	healthRevision      int64
	health              []gatewaymembership.ProtocolObservation
}

type heartbeatPersistence struct {
	fingerprint       [32]byte
	persisted         time.Time
	compilerAttempted time.Time
	inFlight          bool
}

type snapshotReadCache struct {
	version       int
	revision      int64
	transactionID string
	payload       []byte
	identities    *memoryrepo.IdentityReadView
	usage         *memoryrepo.UsageReadView
	forwarding    *memoryrepo.ForwardingReadView
}

func (s *Store) readSnapshot(ctx context.Context, tx *sql.Tx) (*memoryrepo.Store, error) {
	cached, state, err := s.validatedReadSnapshot(ctx, tx)
	if err != nil {
		return nil, err
	}
	if state == nil {
		state, err = decodeReadSnapshot(cached.payload, cached.version, uint64(cached.revision))
		if err != nil {
			return nil, err
		}
	}
	if err := s.mergeCachedForwardingOverlays(ctx, tx, state); err != nil {
		return nil, err
	}
	s.applyHeartbeatTelemetry(state)
	return state, nil
}

func (s *Store) validatedReadSnapshot(ctx context.Context, tx *sql.Tx) (snapshotReadCache, *memoryrepo.Store, error) {
	// Only protect the in-process cache. PostgreSQL validation and snapshot
	// decoding can take milliseconds to seconds and must not serialize all
	// authentication, subscription and panel reads behind one mutex.
	s.readMu.Lock()
	cached := s.readCache
	s.readMu.Unlock()
	var next snapshotReadCache
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT format_version, revision, xmin::text,
 CASE WHEN format_version=$1 AND revision=$2 AND xmin::text=$3 THEN NULL ELSE payload END
 FROM nyvp_control_snapshots WHERE singleton=true`, cached.version, cached.revision, cached.transactionID).
		Scan(&next.version, &next.revision, &next.transactionID, &raw)
	if err != nil {
		return snapshotReadCache{}, nil, errors.New("PostgreSQL snapshot read failed")
	}
	if !memoryrepo.SupportsSnapshotVersion(next.version) {
		return snapshotReadCache{}, nil, errors.New("unsupported PostgreSQL snapshot version")
	}
	if raw == nil && cached.payload != nil {
		// The bytes and detached identity projection are immutable. Database
		// identity is checked even when a request only needs authentication.
		return cached, nil, nil
	}
	// Concurrent readers of one MVCC row share only the immutable projection
	// build. A mutable transaction store is never shared between callers.
	var private *memoryrepo.Store
	key := fmt.Sprintf("%d/%d/%s", next.version, next.revision, next.transactionID)
	building := s.readBuild.DoChan(key, func() (any, error) {
		s.readMu.Lock()
		current := s.readCache
		s.readMu.Unlock()
		if current.payload != nil && current.version == next.version && current.revision == next.revision && current.transactionID == next.transactionID {
			return current, nil
		}
		state, err := decodeReadSnapshot(raw, next.version, uint64(next.revision))
		if err != nil {
			return nil, err
		}
		next.payload = raw
		next.identities = state.IdentityReadView()
		next.usage = state.UsageReadView()
		next.forwarding = state.ForwardingReadView()
		private = state
		// Do not let a slower transaction publish an older snapshot over a newer
		// committed one. Equal revisions with a different xmin are external
		// repairs; the first reader that observes the replacement wins.
		s.readMu.Lock()
		current = s.readCache
		if current.payload == nil || next.revision > current.revision ||
			(next.revision == current.revision && next.transactionID == current.transactionID) ||
			(next.revision == current.revision && current.transactionID == cached.transactionID) {
			s.readCache = next
		}
		s.readMu.Unlock()
		return next, nil
	})
	select {
	case <-ctx.Done():
		return snapshotReadCache{}, nil, ctx.Err()
	case result := <-building:
		if result.Err != nil {
			return snapshotReadCache{}, nil, result.Err
		}
		return result.Val.(snapshotReadCache), private, nil
	}
}

func (s *Store) mergeCachedForwardingOverlays(ctx context.Context, tx *sql.Tx, state *memoryrepo.Store) error {
	traffic, health, err := s.forwardingOverlays(ctx, tx)
	if err != nil {
		return err
	}
	state.MergeRuleTrafficOverlays(traffic)
	state.MergeProtocolHealthOverlays(health)
	return nil
}

func readIdentity[T any](parent context.Context, s *Store, operation func(*memoryrepo.IdentityReadView) (T, error)) (T, error) {
	var zero T
	ctx, cancel := context.WithTimeout(parent, operationTimeout)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return zero, errors.New("PostgreSQL transaction unavailable")
	}
	defer tx.Rollback()
	cached, _, err := s.validatedReadSnapshot(ctx, tx)
	if err != nil {
		return zero, err
	}
	result, err := operation(cached.identities)
	if err != nil {
		return result, err
	}
	if err := tx.Commit(); err != nil {
		return zero, errors.New("PostgreSQL transaction commit failed")
	}
	return result, nil
}

// Each request owns its decoded maps, slices and runtime material. Retaining
// the database payload avoids encoding the entire cache again on every read.
func decodeReadSnapshot(raw []byte, version int, revision uint64) (*memoryrepo.Store, error) {
	clone, err := memoryrepo.DecodeSnapshotAtVersion(raw, version)
	if err != nil {
		return nil, errors.New("PostgreSQL snapshot clone failed")
	}
	clone.SetPersistenceRevision(revision)
	return clone, nil
}

func (s *Store) DesiredConfigChanges(nodeID string) <-chan struct{} {
	return s.desiredChanges.ForNode(nodeID)
}

func (s *Store) ReleaseDesiredConfigChanges(nodeID string, channel <-chan struct{}) {
	s.desiredChanges.Release(nodeID, channel)
}

func Open(ctx context.Context, databaseURL string, bootstrap auth.Administrator, pool ...PoolConfig) (*Store, error) {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, errors.New("invalid PostgreSQL connection configuration")
	}
	settings := PoolConfig{MaxOpenConns: defaultMaxOpenConns, MaxIdleConns: defaultMaxIdleConns}
	if len(pool) > 0 {
		settings = pool[0]
	}
	if settings.MaxOpenConns <= 0 {
		settings.MaxOpenConns = defaultMaxOpenConns
	}
	if settings.MaxIdleConns < 0 {
		settings.MaxIdleConns = 0
	}
	if settings.MaxIdleConns > settings.MaxOpenConns {
		settings.MaxIdleConns = settings.MaxOpenConns
	}
	db.SetMaxOpenConns(settings.MaxOpenConns)
	db.SetMaxIdleConns(settings.MaxIdleConns)
	db.SetConnMaxLifetime(30 * time.Minute)
	store := &Store{db: db, heartbeats: make(map[string]heartbeatPersistence)}
	if err := store.initialize(ctx, bootstrap); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) initialize(parent context.Context, bootstrap auth.Administrator) error {
	ctx, cancel := context.WithTimeout(parent, operationTimeout)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("PostgreSQL connection unavailable")
	}
	defer tx.Rollback()
	// Serialize bootstrap and table creation across accidental concurrent starts.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(1853459821)`); err != nil {
		return errors.New("PostgreSQL initialization lock failed")
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS nyvp_control_snapshots (
		singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
		format_version integer NOT NULL,
		revision bigint NOT NULL CHECK (revision > 0),
		payload bytea NOT NULL CHECK (octet_length(payload) BETWEEN 1 AND 16777216),
		updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
	)`); err != nil {
		return errors.New("PostgreSQL snapshot table initialization failed")
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM nyvp_control_snapshots`).Scan(&count); err != nil {
		return errors.New("PostgreSQL snapshot lookup failed")
	}
	if count == 0 {
		if bootstrap.ID == "" || bootstrap.Username == "" || auth.ValidatePasswordHash(bootstrap.PasswordHash) != nil {
			return errors.New("valid bootstrap administrator required for empty store")
		}
		raw, err := memoryrepo.New(bootstrap).EncodeSnapshot()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO nyvp_control_snapshots (singleton, format_version, revision, payload) VALUES (true, $1, 1, $2)`, memoryrepo.SnapshotVersion, raw); err != nil {
			return errors.New("PostgreSQL initial snapshot write failed")
		}
	}
	if err := initializeRuleTrafficOverlays(ctx, tx); err != nil {
		return err
	}
	if err := initializeProtocolHealthOverlays(ctx, tx); err != nil {
		return err
	}
	if err := initializeProjectionRevisionTracking(ctx, tx); err != nil {
		return err
	}
	if err := initializeAuditJournal(ctx, tx); err != nil {
		return err
	}
	if err := initializeNodeConfigHistory(ctx, tx); err != nil {
		return err
	}
	if err := migrationTables(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return errors.New("PostgreSQL initialization commit failed")
	}
	return nil
}

func readState(ctx context.Context, tx *sql.Tx, lock bool, stores ...*Store) (*memoryrepo.Store, error) {
	var state *memoryrepo.Store
	var err error
	if lock {
		// Decode before taking the shared business row lock. The locked MVCC
		// identity must still match before this private state can be mutated.
		prepared, prepareErr := prepareSnapshotWrite(ctx, tx)
		if prepareErr != nil {
			return nil, prepareErr
		}
		state, err = lockSnapshotWrite(ctx, tx, prepared)
	} else {
		prepared, prepareErr := prepareSnapshotWrite(ctx, tx)
		if prepareErr != nil {
			return nil, prepareErr
		}
		state = prepared.state
	}
	if err == nil {
		if len(stores) > 0 && stores[0] != nil {
			err = stores[0].mergeCachedForwardingOverlays(ctx, tx, state)
		} else {
			err = mergeRuleTrafficOverlays(ctx, tx, state)
			if err == nil {
				err = mergeProtocolHealthOverlays(ctx, tx, state)
			}
		}
	}
	return state, err
}

func transact[T any](parent context.Context, s *Store, write bool, operation func(*memoryrepo.Store) (T, error)) (T, error) {
	var zero T
	ctx, cancel := context.WithTimeout(parent, operationTimeout)
	defer cancel()
	options := &sql.TxOptions{ReadOnly: !write}
	if !write {
		options.Isolation = sql.LevelRepeatableRead
	}
	tx, err := s.db.BeginTx(ctx, options)
	if err != nil {
		return zero, errors.New("PostgreSQL transaction unavailable")
	}
	defer tx.Rollback()
	var state *memoryrepo.Store
	if write {
		state, err = readState(ctx, tx, true, s)
		if err == nil {
			s.applyHeartbeatTelemetry(state)
		}
	} else {
		state, err = s.readSnapshot(ctx, tx)
	}
	if err != nil {
		return zero, err
	}
	attachNodeConfigHistoryReader(ctx, tx, state)
	var before map[string]int64
	state.SetPanelQuotaProvider(s.quotaProvider())
	var beforeUsage panelquota.Usage
	var auditCount int
	var committedCache snapshotReadCache
	if write {
		before = state.DesiredGenerationIndex()
		beforeUsage, _ = state.PanelResourceUsage(ctx)
		auditCount = len(state.AuditEvents())
	}
	result, err := operation(state)
	if err != nil {
		return result, err
	}
	if write {
		events := state.AuditEvents()
		afterUsage, _ := state.PanelResourceUsage(ctx)
		if err := s.validatePanelGrowth(beforeUsage, afterUsage); err != nil {
			return zero, err
		}
		if auditCount > len(events) {
			return zero, errors.New("audit state changed unexpectedly")
		}
		if err := appendAuditJournal(ctx, tx, events[auditCount:]); err != nil {
			return zero, err
		}
		if err := persistNodeConfigHistory(ctx, tx, state.DetachHistoricalNodeConfigs()); err != nil {
			return zero, err
		}
		raw, err := encodeHotSnapshot(state)
		if err != nil {
			return zero, err
		}
		// Proofs were merged before the mutation and are now in the new snapshot.
		// Clear overlays atomically so an old proof cannot undo a business edit.
		if _, err := tx.ExecContext(ctx, `DELETE FROM hl_panel_protocol_health`); err != nil {
			return zero, errors.New("protocol health projection fold failed")
		}
		committedCache.version = memoryrepo.SnapshotVersion
		committedCache.payload = raw
		if err := tx.QueryRowContext(ctx, `UPDATE nyvp_control_snapshots SET payload = $1, format_version = $2, revision = revision + 1, updated_at = clock_timestamp() WHERE singleton = true RETURNING revision, xmin::text`, raw, memoryrepo.SnapshotVersion).
			Scan(&committedCache.revision, &committedCache.transactionID); err != nil {
			return zero, errors.New("PostgreSQL snapshot write failed")
		}
	}
	if err := tx.Commit(); err != nil {
		return zero, errors.New("PostgreSQL transaction commit failed")
	}
	if write {
		// Publish only committed bytes and detached identities. The next request
		// still validates the database row, but need not decode the entire state
		// again merely because this instance just recorded a heartbeat or probe.
		committedCache.identities = state.IdentityReadView()
		committedCache.usage = state.UsageReadView()
		committedCache.forwarding = state.ForwardingReadView()
		s.readMu.Lock()
		if s.readCache.revision <= committedCache.revision {
			s.readCache = committedCache
		}
		s.readMu.Unlock()
		after := state.DesiredGenerationIndex()
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
	return result, nil
}

// transactNoopAware runs a cheap read-only attempt first. Protocol probes are
// frequently stale while a node is restarting; those batches must not acquire
// the snapshot write path or rewrite the full payload. When the operation
// changes protocol health or activation audit state, it is replayed in the
// normal row-locked transaction so concurrent writes remain serialized.
func transactNoopAware[T any](parent context.Context, s *Store, operation func(*memoryrepo.Store) (T, error)) (T, error) {
	var zero T
	ctx, cancel := context.WithTimeout(parent, operationTimeout)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return zero, errors.New("PostgreSQL transaction unavailable")
	}
	defer tx.Rollback()
	state, err := s.readSnapshot(ctx, tx)
	if err != nil {
		return zero, err
	}
	attachNodeConfigHistoryReader(ctx, tx, state)
	beforeRevision, beforeAudits := state.PersistenceMutationState()
	result, err := operation(state)
	if err != nil {
		return result, err
	}
	afterRevision, afterAudits := state.PersistenceMutationState()
	if beforeRevision == afterRevision && beforeAudits == afterAudits {
		if err := tx.Commit(); err != nil {
			return zero, errors.New("PostgreSQL transaction commit failed")
		}
		return result, nil
	}
	// A concurrent writer may have committed while the read-only attempt ran.
	// Re-run the pure in-memory operation against the latest row-locked state.
	// Release the read-only connection before starting the write transaction;
	// otherwise a small pool can deadlock while the deferred rollback waits for
	// the write attempt to finish.
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		return zero, errors.New("PostgreSQL read transaction rollback failed")
	}
	return transact(parent, s, true, operation)
}

func mutate(ctx context.Context, s *Store, operation func(*memoryrepo.Store) error) error {
	_, err := transact(ctx, s, true, func(state *memoryrepo.Store) (struct{}, error) { return struct{}{}, operation(state) })
	return err
}
