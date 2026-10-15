package postgressnapshot

import (
	"context"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/licensing"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
)

type licenseIssueResult struct {
	item     licensing.Code
	replayed bool
}

type licenseRevokeResult struct {
	result   licensing.RevokeResult
	replayed bool
}

func (s *Store) IssueLicenseCode(ctx context.Context, item licensing.Code, event audit.Event, idempotencyKey, requestSHA256 string) (licensing.Code, bool, error) {
	result, err := transact(ctx, s, true, func(state *memoryrepo.Store) (licenseIssueResult, error) {
		item, replayed, err := state.IssueLicenseCode(ctx, item, event, idempotencyKey, requestSHA256)
		return licenseIssueResult{item: item, replayed: replayed}, err
	})
	return result.item, result.replayed, err
}

func (s *Store) ListLicenseCodes(ctx context.Context, now time.Time) ([]licensing.Code, error) {
	return transact(ctx, s, false, func(state *memoryrepo.Store) ([]licensing.Code, error) { return state.ListLicenseCodes(ctx, now) })
}

func (s *Store) RevokeLicenseCode(ctx context.Context, input licensing.RevokeInput, event audit.Event) (licensing.RevokeResult, bool, error) {
	result, err := transact(ctx, s, true, func(state *memoryrepo.Store) (licenseRevokeResult, error) {
		value, replayed, err := state.RevokeLicenseCode(ctx, input, event)
		return licenseRevokeResult{result: value, replayed: replayed}, err
	})
	return result.result, result.replayed, err
}

func (s *Store) ActivateLicenseCode(ctx context.Context, code, domain, instanceID string, now time.Time, event audit.Event) (licensing.Lease, error) {
	return transact(ctx, s, true, func(state *memoryrepo.Store) (licensing.Lease, error) {
		return state.ActivateLicenseCode(ctx, code, domain, instanceID, now, event)
	})
}

func (s *Store) RenewLicense(ctx context.Context, licenseID, instanceID, code, domain string, now time.Time, event audit.Event) (licensing.Lease, error) {
	return transact(ctx, s, true, func(state *memoryrepo.Store) (licensing.Lease, error) {
		return state.RenewLicense(ctx, licenseID, instanceID, code, domain, now, event)
	})
}

func (s *Store) CurrentLicenseLease(ctx context.Context) (licensing.Lease, bool, error) {
	result, err := transact(ctx, s, false, func(state *memoryrepo.Store) (struct {
		lease licensing.Lease
		ok    bool
	}, error) {
		lease, ok, err := state.CurrentLicenseLease(ctx)
		return struct {
			lease licensing.Lease
			ok    bool
		}{lease, ok}, err
	})
	return result.lease, result.ok, err
}

func (s *Store) StoreLicenseLease(ctx context.Context, lease licensing.Lease) error {
	return mutate(ctx, s, func(state *memoryrepo.Store) error {
		return state.StoreLicenseLease(ctx, lease)
	})
}
