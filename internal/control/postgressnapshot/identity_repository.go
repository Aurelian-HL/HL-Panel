package postgressnapshot

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/control/nodes"
)

var _ auth.Repository = (*Store)(nil)
var _ audit.Repository = (*Store)(nil)
var _ enrollment.Repository = (*Store)(nil)
var _ nodes.Repository = (*Store)(nil)

func (s *Store) AdministratorByUsername(ctx context.Context, username string) (auth.Administrator, error) {
	return transact(ctx, s, false, func(state *memoryrepo.Store) (auth.Administrator, error) {
		return state.AdministratorByUsername(ctx, username)
	})
}

func (s *Store) AdministratorByID(ctx context.Context, id string) (auth.Administrator, error) {
	return transact(ctx, s, false, func(state *memoryrepo.Store) (auth.Administrator, error) {
		return state.AdministratorByID(ctx, id)
	})
}

func (s *Store) UpdateAdministratorPassword(ctx context.Context, id string, expectedHash, newHash []byte, mustChangePassword bool, event audit.Event) error {
	return mutate(ctx, s, func(state *memoryrepo.Store) error {
		return state.UpdateAdministratorPassword(ctx, id, expectedHash, newHash, mustChangePassword, event)
	})
}

func (s *Store) CreateSession(ctx context.Context, session auth.Session, event audit.Event) error {
	return mutate(ctx, s, func(state *memoryrepo.Store) error { return state.CreateSession(ctx, session, event) })
}

func (s *Store) SessionByTokenHash(ctx context.Context, hash string, now time.Time) (auth.Session, error) {
	return transact(ctx, s, false, func(state *memoryrepo.Store) (auth.Session, error) { return state.SessionByTokenHash(ctx, hash, now) })
}

func (s *Store) AppendAudit(ctx context.Context, event audit.Event) error {
	return mutate(ctx, s, func(state *memoryrepo.Store) error { return state.AppendAudit(ctx, event) })
}

func (s *Store) AuditEvents(ctx context.Context) ([]audit.Event, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, errors.New("audit journal transaction unavailable")
	}
	defer tx.Rollback()
	events, err := readAuditJournal(ctx, tx)
	if err != nil {
		return nil, err
	}
	state, err := s.readSnapshot(ctx, tx)
	if err != nil {
		return nil, err
	}
	events = append(events, state.AuditEvents()...)
	if err := tx.Commit(); err != nil {
		return nil, errors.New("audit journal transaction failed")
	}
	return events, nil
}

func (s *Store) CreateEnrollmentToken(ctx context.Context, token enrollment.Token, event audit.Event) error {
	return mutate(ctx, s, func(state *memoryrepo.Store) error { return state.CreateEnrollmentToken(ctx, token, event) })
}

func (s *Store) CreateEnrollmentTokenIdempotent(ctx context.Context, token enrollment.Token, event audit.Event) (enrollment.Token, bool, error) {
	type result struct {
		token    enrollment.Token
		replayed bool
	}
	v, err := transact(ctx, s, true, func(state *memoryrepo.Store) (result, error) {
		value, replayed, err := state.CreateEnrollmentTokenIdempotent(ctx, token, event)
		return result{token: value, replayed: replayed}, err
	})
	return v.token, v.replayed, err
}

func (s *Store) ListPendingEnrollmentTokens(ctx context.Context, groupID string, now time.Time) ([]enrollment.PendingToken, error) {
	return transact(ctx, s, false, func(state *memoryrepo.Store) ([]enrollment.PendingToken, error) {
		return state.ListPendingEnrollmentTokens(ctx, groupID, now)
	})
}

func (s *Store) ConsumeEnrollmentToken(ctx context.Context, input enrollment.ConsumeInput, now time.Time, event audit.Event) (nodes.Node, error) {
	return transact(ctx, s, true, func(state *memoryrepo.Store) (nodes.Node, error) {
		return state.ConsumeEnrollmentToken(ctx, input, now, event)
	})
}

func (s *Store) RevokeEnrollmentToken(ctx context.Context, input enrollment.RevokeInput, event audit.Event) (enrollment.RevokeResult, bool, error) {
	type result struct {
		revoked  enrollment.RevokeResult
		replayed bool
	}
	value, err := transact(ctx, s, true, func(state *memoryrepo.Store) (result, error) {
		revoked, replayed, err := state.RevokeEnrollmentToken(ctx, input, event)
		return result{revoked: revoked, replayed: replayed}, err
	})
	return value.revoked, value.replayed, err
}

func (s *Store) NodeByCredentialHash(ctx context.Context, hash string) (nodes.Node, error) {
	return transact(ctx, s, false, func(state *memoryrepo.Store) (nodes.Node, error) { return state.NodeByCredentialHash(ctx, hash) })
}

func (s *Store) ListNodes(ctx context.Context) ([]nodes.Node, error) {
	return transact(ctx, s, false, func(state *memoryrepo.Store) ([]nodes.Node, error) { return state.ListNodes(ctx) })
}

func (s *Store) ListNezhaBindings(ctx context.Context) (map[string]uint64, error) {
	return transact(ctx, s, false, func(state *memoryrepo.Store) (map[string]uint64, error) {
		return state.ListNezhaBindings(ctx)
	})
}

func (s *Store) RotateCredential(ctx context.Context, input nodes.RotateCredentialInput, event audit.Event) (nodes.CredentialRotationResult, bool, error) {
	type result struct {
		rotation nodes.CredentialRotationResult
		replayed bool
	}
	value, err := transact(ctx, s, true, func(state *memoryrepo.Store) (result, error) {
		rotation, replayed, err := state.RotateCredential(ctx, input, event)
		return result{rotation: rotation, replayed: replayed}, err
	})
	return value.rotation, value.replayed, err
}

func (s *Store) UpdateHeartbeat(ctx context.Context, id string, heartbeat nodes.Heartbeat, now time.Time, event audit.Event) (nodes.Node, error) {
	return transact(ctx, s, true, func(state *memoryrepo.Store) (nodes.Node, error) {
		return state.UpdateHeartbeat(ctx, id, heartbeat, now, event)
	})
}

func (s *Store) Overview(ctx context.Context, now time.Time, onlineFor time.Duration) (nodes.Overview, error) {
	return transact(ctx, s, false, func(state *memoryrepo.Store) (nodes.Overview, error) { return state.Overview(ctx, now, onlineFor) })
}

func (s *Store) RequestControl(ctx context.Context, input nodes.ControlCommandInput, event audit.Event) (nodes.ControlCommandResult, bool, error) {
	type result struct {
		value    nodes.ControlCommandResult
		replayed bool
	}
	v, err := transact(ctx, s, true, func(state *memoryrepo.Store) (result, error) {
		value, replayed, err := state.RequestControl(ctx, input, event)
		return result{value, replayed}, err
	})
	return v.value, v.replayed, err
}
func (s *Store) ControlForNode(ctx context.Context, nodeID string) (nodes.ControlCommandResult, error) {
	return transact(ctx, s, false, func(state *memoryrepo.Store) (nodes.ControlCommandResult, error) {
		return state.ControlForNode(ctx, nodeID)
	})
}
func (s *Store) RecordControlResult(ctx context.Context, nodeID string, result nodes.ControlCommandResult, event audit.Event) error {
	return mutate(ctx, s, func(state *memoryrepo.Store) error { return state.RecordControlResult(ctx, nodeID, result, event) })
}

func (s *Store) DeleteOffline(ctx context.Context, input nodes.DeleteInput, event audit.Event) (nodes.DeleteResult, bool, error) {
	type result struct {
		value    nodes.DeleteResult
		replayed bool
	}
	v, err := transact(ctx, s, true, func(state *memoryrepo.Store) (result, error) {
		value, replayed, err := state.DeleteOffline(ctx, input, event)
		return result{value, replayed}, err
	})
	return v.value, v.replayed, err
}
