package memoryrepo

import (
	"bytes"
	"context"
	"errors"
	"math"
	"testing"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/customeridentity"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/subscriptions"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
	"github.com/hongle/hl-panel/internal/control/vlessruntime"
)

func TestCustomerDeletionReferencesAndRevisionAreAtomic(t *testing.T) {
	for _, relation := range []string{"rule", "owner", "subscription", "identity", "runtime", "revision", "missing"} {
		t.Run(relation, func(t *testing.T) {
			s, item := customerRepositoryFixture(t)
			seedCustomer(s, item)
			input := customers.DeleteInput{ID: item.ID, DeletedBy: "admin-test", ExpectedRevision: 1, IdempotencyKey: "delete-user", RequestSHA256: "delete-hash"}
			var want error
			if relation == "revision" {
				want = faults.ErrConflict
			}
			switch relation {
			case "rule":
				s.forwardRules["rule"] = forwarding.Rule{CustomerID: item.ID}
			case "owner":
				s.forwardRules["rule"] = forwarding.Rule{OwnerKind: forwarding.OwnerCustomer, OwnerID: item.ID}
			case "subscription":
				s.subscriptions["sub"] = subscriptions.Record{Item: subscriptions.Item{CustomerID: item.ID}}
			case "identity":
				s.vlessBindings["binding"] = vlessidentity.CredentialRecord{Binding: vlessidentity.Binding{CustomerID: item.ID, State: vlessidentity.StateActive}}
			case "runtime":
				s.vlessRuntimeMaterials["runtime"] = vlessruntime.Material{CustomerID: item.ID, State: vlessruntime.StateActive}
			case "revision":
				input.ExpectedRevision = 2
			case "missing":
				input.ID = "missing-customer"
				want = faults.ErrNotFound
			}
			before, err := s.EncodeSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.DeleteCustomer(context.Background(), input, audit.Event{}); !errors.Is(err, want) {
				t.Fatalf("expected deletion reference/revision rejection: %v", err)
			}
			after, err := s.EncodeSnapshot()
			if err != nil || want != nil && !bytes.Equal(before, after) || want == nil && !bytes.Equal(protectedServiceState(t, before), protectedServiceState(t, after)) {
				t.Fatal("rejected deletion changed business state, sessions, audit or idempotency")
			}
		})
	}
}

func TestCustomerDeletionRevokesSessionsAndRetainsHistoryAfterRestore(t *testing.T) {
	ctx := context.Background()
	s, item := customerRepositoryFixture(t)
	seedCustomer(s, item)
	other := item
	other.ID, other.Username = "other-customer", "other-customer"
	seedCustomer(s, other)
	identity, err := customeridentity.NewService(s.CustomerIdentityRepository("test"), nil, customeridentity.Options{PasswordFingerprintKey: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	var tokens []string
	for range 2 {
		login, err := identity.Login(ctx, item.Username, "customer-test-password")
		if err != nil {
			t.Fatal(err)
		}
		tokens = append(tokens, login.AccessToken)
	}
	otherLogin, err := identity.Login(ctx, other.Username, "customer-test-password")
	if err != nil {
		t.Fatal(err)
	}
	revokedAt := item.UpdatedAt
	s.vlessBindings["historical"] = vlessidentity.CredentialRecord{Binding: vlessidentity.Binding{
		ID: "historical", CustomerID: item.ID, ForwardingRuleID: "deleted-rule", EndpointPoolID: "deleted-pool",
		State: vlessidentity.StateRevoked, Revision: 2, CreatedAt: item.CreatedAt, UpdatedAt: revokedAt, RevokedAt: &revokedAt,
	}}
	s.vlessRuntimeMaterials["historical-runtime"] = vlessruntime.Material{
		ID: "historical-runtime", BindingID: "historical", NodeID: "deleted-node", CustomerID: item.ID,
		ForwardingRuleID: "deleted-rule", EndpointPoolID: "deleted-pool", State: vlessruntime.StateRevoked,
		Revision: 2, CreatedAt: item.CreatedAt, UpdatedAt: revokedAt, RevokedAt: &revokedAt,
	}
	beforeAudits := len(s.AuditEvents())
	service := customers.NewService(s, nil)
	request := customers.DeleteRequest{Revision: item.Revision}
	if replayed, err := service.DeleteCustomer(ctx, "admin-test", item.ID, request, "delete-customer"); err != nil || replayed {
		t.Fatal("deletion failed", err)
	}
	if len(s.customerSessions) != 1 || len(s.customers) != 2 || len(s.panelAccounts) != 1 || len(s.auditEvents) != beforeAudits+1 || s.auditEvents[beforeAudits].Action != "customer.delete" {
		t.Fatal("customer/session/audit deletion inconsistent")
	}
	for _, token := range tokens {
		if _, err := identity.Authenticate(ctx, token); !errors.Is(err, faults.ErrUnauthorized) {
			t.Fatal("deleted customer session remains authorized", err)
		}
	}
	if _, err := identity.Authenticate(ctx, otherLogin.AccessToken); err != nil {
		t.Fatal("another customer's session was revoked", err)
	}
	raw, err := s.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeSnapshot(raw)
	if err != nil {
		t.Fatal("deletion history could not restore", err)
	}
	if _, err := newCustomerIdentityService(t, restored, item.CreatedAt).Login(ctx, item.Username, "customer-test-password"); !errors.Is(err, faults.ErrUnauthorized) {
		t.Fatal("revoked login returned after restart", err)
	}
	restoredService := customers.NewService(restored, nil)
	auditsBeforeReplay := len(restored.auditEvents)
	if replayed, err := restoredService.DeleteCustomer(ctx, "admin-test", item.ID, request, "delete-customer"); err != nil || !replayed {
		t.Fatal("persisted deletion replay failed", err)
	}
	if len(restored.auditEvents) != auditsBeforeReplay || len(restored.vlessBindings) != 1 || len(restored.vlessRuntimeMaterials) != 1 {
		t.Fatal("replay changed audit or deleted revoked history")
	}
	if _, err := restoredService.DeleteCustomer(ctx, "admin-test", other.ID, request, "delete-customer"); !errors.Is(err, faults.ErrIdempotencyConflict) {
		t.Fatal("same key accepted a different customer", err)
	}
	if _, err := restoredService.DeleteCustomer(ctx, "admin-test", item.ID, customers.DeleteRequest{Revision: 2}, "delete-customer"); !errors.Is(err, faults.ErrIdempotencyConflict) {
		t.Fatal("same key accepted a different revision", err)
	}
	if _, err := restored.Customer(ctx, other.ID); err != nil {
		t.Fatal("other customer was removed", err)
	}
}

func TestCustomerDeletionValidatesActorRevisionAndKey(t *testing.T) {
	s, item := customerRepositoryFixture(t)
	seedCustomer(s, item)
	for _, test := range []struct {
		actor, id, key string
		revision       int64
	}{
		{"", item.ID, "delete-valid-key", 1},
		{"invalid actor", item.ID, "delete-valid-key", 1},
		{"admin-test", "invalid/id", "delete-valid-key", 1},
		{"admin-test", item.ID, "short", 1},
		{"admin-test", item.ID, "delete valid key", 1},
		{"admin-test", item.ID, "delete-valid-key", 0},
		{"admin-test", item.ID, "delete-valid-key", -1},
		{"admin-test", item.ID, "delete-valid-key", math.MaxInt64},
	} {
		if _, err := customers.NewService(s, nil).DeleteCustomer(context.Background(), test.actor, test.id, customers.DeleteRequest{Revision: test.revision}, test.key); !errors.Is(err, faults.ErrValidation) {
			t.Fatal("invalid deletion request was accepted", err)
		}
	}
	if len(s.customers) != 1 || len(s.auditEvents) != 0 || len(s.businessIdempotency) != 0 {
		t.Fatal("invalid deletion request changed state")
	}
}
