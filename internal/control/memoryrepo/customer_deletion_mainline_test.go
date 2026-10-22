package memoryrepo

import (
	"context"
	"errors"
	"testing"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/customeridentity"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/groupconfig"
	"github.com/hongle/hl-panel/internal/control/subscriptions"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
)

func TestMainlineCustomerDeletionReferencesAndRevisionAreAtomic(t *testing.T) {
	for _, relation := range []string{"rule", "owner", "subscription", "identity", "revision"} {
		t.Run(relation, func(t *testing.T) {
			s, item := customerRepositoryFixture(t)
			seedCustomer(s, item)
			input := customers.DeleteInput{ID: item.ID, DeletedBy: "admin-test", ExpectedRevision: 1, IdempotencyKey: "delete-user", RequestSHA256: "delete-hash"}
			switch relation {
			case "rule":
				s.forwardRules["rule"] = forwarding.Rule{CustomerID: item.ID}
			case "owner":
				s.forwardRules["rule"] = forwarding.Rule{OwnerKind: forwarding.OwnerCustomer, OwnerID: item.ID}
			case "subscription":
				s.subscriptions["sub"] = subscriptions.Record{Item: subscriptions.Item{CustomerID: item.ID}}
			case "identity":
				s.vlessBindings["binding"] = vlessidentity.CredentialRecord{Binding: vlessidentity.Binding{CustomerID: item.ID}}
			case "revision":
				input.ExpectedRevision = 2
			}
			var want error
			if relation == "revision" {
				want = faults.ErrConflict
			}
			if _, err := s.DeleteCustomer(context.Background(), input, audit.Event{}); !errors.Is(err, want) {
				t.Fatalf("expected reference/revision conflict: %v", err)
			}
			wantMutations := 1
			if want != nil {
				wantMutations = 0
			}
			if len(s.customers) != 1 || len(s.auditEvents) != wantMutations || len(s.businessIdempotency) != wantMutations {
				t.Fatal("failed delete changed state")
			}
		})
	}
}

func TestMainlineCustomerDeletionRevokesSessionsAndPersistsReplay(t *testing.T) {
	ctx := context.Background()
	s, item := customerRepositoryFixture(t)
	seedCustomer(s, item)
	s.customerSessions["session"] = customeridentity.Session{CustomerID: item.ID}
	revokedAt := item.UpdatedAt
	s.vlessBindings["historical"] = vlessidentity.CredentialRecord{Binding: vlessidentity.Binding{
		ID: "historical", CustomerID: item.ID, ForwardingRuleID: "deleted-rule", EndpointPoolID: "deleted-pool",
		State: vlessidentity.StateRevoked, Revision: 2, CreatedAt: item.CreatedAt, UpdatedAt: revokedAt, RevokedAt: &revokedAt,
	}}
	service := customers.NewService(s, nil)
	request := customers.DeleteRequest{Revision: 1}
	if replayed, err := service.DeleteCustomer(ctx, "admin-test", item.ID, request, "delete-customer"); err != nil || replayed {
		t.Fatalf("delete: %v", err)
	}
	if len(s.customerSessions) != 0 || len(s.customers) != 1 || len(s.panelAccounts) != 0 || len(s.auditEvents) != 1 || s.auditEvents[0].Action != "customer.delete" {
		t.Fatal("customer/session/audit deletion inconsistent")
	}
	data, err := s.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeSnapshot(data)
	if err != nil {
		t.Fatal(err)
	}
	if replayed, err := customers.NewService(restored, nil).DeleteCustomer(ctx, "admin-test", item.ID, request, "delete-customer"); err != nil || !replayed {
		t.Fatalf("persisted replay: %v", err)
	}
	if len(restored.auditEvents) != 1 {
		t.Fatal("duplicate audit on replay")
	}
	if len(restored.vlessBindings) != 1 {
		t.Fatal("deletion discarded revoked identity history")
	}
	if _, err := service.DeleteCustomer(ctx, "admin-test", "other-customer", request, "delete-customer"); !errors.Is(err, faults.ErrIdempotencyConflict) {
		t.Fatalf("reused key accepted another resource: %v", err)
	}
}

func TestMainlineUserGroupDeletionReferencesAndReplay(t *testing.T) {
	for _, relation := range []string{"customer", "device", "network", "revision", "none"} {
		t.Run(relation, func(t *testing.T) {
			s, item := customerRepositoryFixture(t)
			input := customers.DeleteInput{ID: item.UserGroupID, DeletedBy: "admin-test", ExpectedRevision: 1, IdempotencyKey: "delete-group", RequestSHA256: "delete-group-hash"}
			switch relation {
			case "customer":
				seedCustomer(s, item)
			case "device":
				group := s.deviceGroups["entry-1"]
				group.UserGroupID = item.UserGroupID
				s.deviceGroups[group.ID] = group
			case "network":
				s.groupNetworks["entry-1"] = groupconfig.GroupNetwork{AllowedUserGroupIDs: []string{item.UserGroupID}}
			case "revision":
				input.ExpectedRevision = 2
			}
			_, err := s.DeleteUserGroup(context.Background(), input, audit.Event{})
			if relation != "none" {
				if !errors.Is(err, faults.ErrConflict) || len(s.userGroups) != 1 || len(s.auditEvents) != 0 {
					t.Fatalf("reference delete was not atomic: %v", err)
				}
				return
			}
			if err != nil || len(s.userGroups) != 0 {
				t.Fatalf("delete: %v", err)
			}
			if replayed, err := s.DeleteUserGroup(context.Background(), input, audit.Event{}); err != nil || !replayed || len(s.auditEvents) != 1 {
				t.Fatalf("replay: %v", err)
			}
		})
	}
}
