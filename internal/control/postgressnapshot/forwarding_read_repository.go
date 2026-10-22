package postgressnapshot

import (
	"context"
	"database/sql"
	"errors"

	"github.com/hongle/hl-panel/internal/control/memoryrepo"
)

// Validate the authoritative row identity even for cache hits. Health, usage and
// heartbeat overlays are read in the same transaction and merged into private
// request maps. Activation results and lease expiry are never cached.
func readForwarding[T any](parent context.Context, s *Store, operation func(*memoryrepo.ForwardingReadView) (T, error)) (T, error) {
	return readForwardingView(parent, s, true, operation)
}

// Configuration reads use durable desired/applied evidence only. Telemetry,
// traffic counters and health leases cannot alter a node assignment, so avoid
// fetching and copying their full overlay maps on each agent watch wake-up.
func readConfiguration[T any](parent context.Context, s *Store, operation func(*memoryrepo.ForwardingReadView) (T, error)) (T, error) {
	return readForwardingView(parent, s, false, operation)
}

func readForwardingView[T any](parent context.Context, s *Store, overlays bool, operation func(*memoryrepo.ForwardingReadView) (T, error)) (T, error) {
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
	view := cached.forwarding
	if overlays {
		traffic, health, err := s.forwardingOverlays(ctx, tx)
		if err != nil {
			return zero, err
		}
		view = view.WithOverlays(uint64(cached.revision), traffic, health, s.heartbeatTelemetrySamples())
	}
	result, err := operation(view)
	if err != nil {
		return result, err
	}
	if err := tx.Commit(); err != nil {
		return zero, errors.New("PostgreSQL transaction commit failed")
	}
	return result, nil
}
