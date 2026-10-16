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
	traffic, err := s.ruleTrafficOverlays(ctx, tx)
	if err != nil {
		return zero, err
	}
	health, err := s.protocolHealthOverlays(ctx, tx)
	if err != nil {
		return zero, err
	}
	view := cached.forwarding.WithOverlays(uint64(cached.revision), traffic, health, s.heartbeatTelemetrySamples())
	result, err := operation(view)
	if err != nil {
		return result, err
	}
	if err := tx.Commit(); err != nil {
		return zero, errors.New("PostgreSQL transaction commit failed")
	}
	return result, nil
}
