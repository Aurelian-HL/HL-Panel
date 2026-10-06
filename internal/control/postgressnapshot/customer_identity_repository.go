package postgressnapshot

import (
	"context"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/customeridentity"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
)

type customerIdentityRepository struct {
	store        *Store
	panelVersion string
}

var _ customeridentity.Repository = (*customerIdentityRepository)(nil)

// CustomerIdentityRepository keeps customer authentication inside the same
// serializable snapshot transaction as customer password and audit changes.
func (s *Store) CustomerIdentityRepository(panelVersion string) customeridentity.Repository {
	return &customerIdentityRepository{store: s, panelVersion: panelVersion}
}

func (r *customerIdentityRepository) repository(state *memoryrepo.Store) customeridentity.Repository {
	return state.CustomerIdentityRepository(r.panelVersion)
}

func (r *customerIdentityRepository) CustomerByUsername(ctx context.Context, username string) (customeridentity.CustomerRecord, error) {
	return transact(ctx, r.store, false, func(state *memoryrepo.Store) (customeridentity.CustomerRecord, error) {
		return r.repository(state).CustomerByUsername(ctx, username)
	})
}

func (r *customerIdentityRepository) CustomerByID(ctx context.Context, id string) (customeridentity.CustomerRecord, error) {
	return transact(ctx, r.store, false, func(state *memoryrepo.Store) (customeridentity.CustomerRecord, error) {
		return r.repository(state).CustomerByID(ctx, id)
	})
}

func (r *customerIdentityRepository) CreateSession(ctx context.Context, session customeridentity.Session, event audit.Event) error {
	return mutate(ctx, r.store, func(state *memoryrepo.Store) error {
		return r.repository(state).CreateSession(ctx, session, event)
	})
}

func (r *customerIdentityRepository) SessionByTokenHash(ctx context.Context, hash string, now time.Time) (customeridentity.Session, error) {
	return transact(ctx, r.store, false, func(state *memoryrepo.Store) (customeridentity.Session, error) {
		return r.repository(state).SessionByTokenHash(ctx, hash, now)
	})
}

func (r *customerIdentityRepository) RevokeSession(ctx context.Context, hash, customerID string, event audit.Event) error {
	return mutate(ctx, r.store, func(state *memoryrepo.Store) error {
		return r.repository(state).RevokeSession(ctx, hash, customerID, event)
	})
}

func (r *customerIdentityRepository) PasswordChangeReplay(ctx context.Context, customerID, key, fingerprint string) (bool, error) {
	return transact(ctx, r.store, false, func(state *memoryrepo.Store) (bool, error) {
		return r.repository(state).PasswordChangeReplay(ctx, customerID, key, fingerprint)
	})
}

func (r *customerIdentityRepository) ChangePassword(ctx context.Context, input customeridentity.ChangePasswordInput, event audit.Event) (bool, error) {
	return transact(ctx, r.store, true, func(state *memoryrepo.Store) (bool, error) {
		return r.repository(state).ChangePassword(ctx, input, event)
	})
}

func (r *customerIdentityRepository) ListRulesByCustomer(ctx context.Context, customerID string) ([]customeridentity.RuleRecord, error) {
	return transact(ctx, r.store, false, func(state *memoryrepo.Store) ([]customeridentity.RuleRecord, error) {
		return r.repository(state).ListRulesByCustomer(ctx, customerID)
	})
}

func (r *customerIdentityRepository) RuleOptionsByCustomer(ctx context.Context, customerID string) (customeridentity.RuleOptionsView, error) {
	return transact(ctx, r.store, false, func(state *memoryrepo.Store) (customeridentity.RuleOptionsView, error) {
		return r.repository(state).RuleOptionsByCustomer(ctx, customerID)
	})
}

func (r *customerIdentityRepository) CustomerUsage(ctx context.Context, customerID string) (customeridentity.UsageRecord, error) {
	return transact(ctx, r.store, false, func(state *memoryrepo.Store) (customeridentity.UsageRecord, error) {
		return r.repository(state).CustomerUsage(ctx, customerID)
	})
}

func (r *customerIdentityRepository) ListSubscriptionsByCustomer(ctx context.Context, customerID string) ([]customeridentity.SubscriptionRecord, error) {
	return transact(ctx, r.store, false, func(state *memoryrepo.Store) ([]customeridentity.SubscriptionRecord, error) {
		return r.repository(state).ListSubscriptionsByCustomer(ctx, customerID)
	})
}

func (r *customerIdentityRepository) Portal(ctx context.Context) (customeridentity.PortalView, error) {
	return transact(ctx, r.store, false, func(state *memoryrepo.Store) (customeridentity.PortalView, error) {
		return r.repository(state).Portal(ctx)
	})
}

func (r *customerIdentityRepository) AppendAudit(ctx context.Context, event audit.Event) error {
	return mutate(ctx, r.store, func(state *memoryrepo.Store) error {
		return r.repository(state).AppendAudit(ctx, event)
	})
}
