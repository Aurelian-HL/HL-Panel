package licensing_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/licensing"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
)

func TestLicenseLifecycleSeparatesIssuerAndTargetLease(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	hash, err := auth.HashPassword("unit-test-password")
	if err != nil {
		t.Fatal(err)
	}
	store := memoryrepo.New(auth.Administrator{ID: "admin-1", Username: "admin", PasswordHash: hash})
	service := licensing.NewService(store, func() time.Time { return now })

	item, rawCode, replayed, err := service.Issue(ctx, "admin-1", licensing.IssueInput{Domain: "Panel.Example.com.", Months: 1}, "issue-license-1")
	if err != nil {
		t.Fatal(err)
	}
	if replayed {
		t.Fatal("first issue was incorrectly marked as replayed")
	}
	if item.Domain != "panel.example.com" || rawCode == "" {
		t.Fatalf("issue result = %#v, code present = %t", item, rawCode != "")
	}

	issuerLease, err := service.Activate(ctx, rawCode, "panel.example.com", "target-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := service.CurrentLease(ctx); err != nil || ok {
		t.Fatalf("issuer activation changed local lease: lease active=%t, err=%v", ok, err)
	}

	if err := service.StoreLease(ctx, issuerLease); err != nil {
		t.Fatal(err)
	}
	stored, ok, err := service.CurrentLease(ctx)
	if err != nil || !ok || stored.InstanceID != "target-1" {
		t.Fatalf("target lease was not stored: %#v, active=%t, err=%v", stored, ok, err)
	}

	snapshot, err := store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(snapshot, []byte(rawCode)) {
		t.Fatal("plaintext authorization code was persisted")
	}
	restored, err := memoryrepo.DecodeSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	restoredService := licensing.NewService(restored, func() time.Time { return now })
	if lease, ok, err := restoredService.CurrentLease(ctx); err != nil || !ok || lease.LicenseID != issuerLease.LicenseID {
		t.Fatalf("target lease was not restored: %#v, active=%t, err=%v", lease, ok, err)
	}

	if _, err := service.Activate(ctx, rawCode, "other.example.com", "target-2"); !errors.Is(err, licensing.ErrDomainMismatch) {
		t.Fatalf("domain mismatch error = %v", err)
	}
	if _, err := service.Renew(ctx, issuerLease.LicenseID, "target-1", rawCode, "panel.example.com"); err != nil {
		t.Fatalf("renew before expiry failed: %v", err)
	}

	if _, replayed, err := service.Revoke(ctx, "admin-1", item.ID, "revoke-license-1"); err != nil || replayed {
		t.Fatalf("revoke failed: replayed=%t err=%v", replayed, err)
	}
	if _, err := service.Renew(ctx, issuerLease.LicenseID, "target-1", rawCode, "panel.example.com"); !errors.Is(err, licensing.ErrRevoked) {
		t.Fatalf("revoked renewal error = %v", err)
	}
}

func TestCurrentLeaseExpiresWithoutDeletingPersistedLease(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	hash, err := auth.HashPassword("unit-test-password")
	if err != nil {
		t.Fatal(err)
	}
	store := memoryrepo.New(auth.Administrator{ID: "admin-1", Username: "admin", PasswordHash: hash})
	service := licensing.NewService(store, func() time.Time { return now })
	_, rawCode, replayed, err := service.Issue(ctx, "admin-1", licensing.IssueInput{Domain: "198.51.100.10", Months: 1}, "issue-license-expiry")
	if err != nil {
		t.Fatal(err)
	}
	if replayed {
		t.Fatal("first issue was incorrectly marked as replayed")
	}
	lease, err := service.Activate(ctx, rawCode, "198.51.100.10", "target-expiry")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.StoreLease(ctx, lease); err != nil {
		t.Fatal(err)
	}
	now = lease.ExpiresAt.Add(time.Second)
	if _, ok, err := service.CurrentLease(ctx); !errors.Is(err, licensing.ErrExpired) || ok {
		t.Fatalf("expired lease result active=%t, err=%v", ok, err)
	}
}

func TestIssueIsIdempotentWithoutPersistingPlaintextCode(t *testing.T) {
	ctx := context.Background()
	hash, err := auth.HashPassword("unit-test-password")
	if err != nil {
		t.Fatal(err)
	}
	store := memoryrepo.New(auth.Administrator{ID: "admin-1", Username: "admin", PasswordHash: hash})
	service := licensing.NewService(store, func() time.Time { return time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC) })

	first, raw, replayed, err := service.Issue(ctx, "admin-1", licensing.IssueInput{Domain: "panel.example.com", Months: 1}, "issue-replay")
	if err != nil || replayed || raw == "" {
		t.Fatalf("first issue = item=%#v raw=%q replayed=%t err=%v", first, raw, replayed, err)
	}
	second, replayRaw, replayed, err := service.Issue(ctx, "admin-1", licensing.IssueInput{Domain: "PANEL.EXAMPLE.COM.", Months: 1}, "issue-replay")
	if err != nil || !replayed || replayRaw != "" || second.ID != first.ID {
		t.Fatalf("replayed issue = item=%#v raw=%q replayed=%t err=%v", second, replayRaw, replayed, err)
	}
	if _, _, _, err := service.Issue(ctx, "admin-1", licensing.IssueInput{Domain: "other.example.com", Months: 1}, "issue-replay"); !errors.Is(err, faults.ErrIdempotencyConflict) {
		t.Fatalf("same key with different request error = %v", err)
	}
	items, err := service.List(ctx)
	if err != nil || len(items) != 1 {
		t.Fatalf("license count after replay = %d, err=%v", len(items), err)
	}
	if bytes.Contains(mustSnapshot(t, store), []byte(raw)) {
		t.Fatal("plaintext authorization code was persisted in idempotency record")
	}
}

func TestRevokeIsIdempotentAndRequiresKey(t *testing.T) {
	ctx := context.Background()
	store := memoryrepo.New(auth.Administrator{ID: "admin-1", Username: "admin"})
	now := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	service := licensing.NewService(store, func() time.Time { return now })
	item, _, _, err := service.Issue(ctx, "admin-1", licensing.IssueInput{Domain: "panel.example.com", Months: 1}, "issue-revoke-test")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Revoke(ctx, "admin-1", item.ID, ""); !errors.Is(err, faults.ErrValidation) {
		t.Fatalf("missing key error = %v", err)
	}
	if _, replayed, err := service.Revoke(ctx, "admin-1", item.ID, "revoke-replay"); err != nil || replayed {
		t.Fatalf("first revoke = replayed=%t err=%v", replayed, err)
	}
	auditCount := len(store.AuditEvents())
	if _, replayed, err := service.Revoke(ctx, "admin-1", item.ID, "revoke-replay"); err != nil || !replayed {
		t.Fatalf("replayed revoke = replayed=%t err=%v", replayed, err)
	}
	if len(store.AuditEvents()) != auditCount {
		t.Fatalf("replayed revoke appended audit event: before=%d after=%d", auditCount, len(store.AuditEvents()))
	}
}

func mustSnapshot(t *testing.T, store *memoryrepo.Store) []byte {
	t.Helper()
	raw, err := store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
