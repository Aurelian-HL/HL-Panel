package memoryrepo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/customeridentity"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/rulegroups"
	"github.com/hongle/hl-panel/internal/control/subscriptions"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
	"github.com/hongle/hl-panel/internal/control/vlessruntime"
)

func TestServiceOwnerDeletionAfterRuleWithdrawalPublishesNoExtraGeneration(t *testing.T) {
	f := newOwnedVLESSBundleFixture(t, true, "cus-1")
	ctx := context.Background()
	service := customers.NewService(f.store, nil)
	owner, err := f.store.Customer(ctx, f.rule.CustomerID)
	if err != nil {
		t.Fatal(err)
	}
	request := customers.DeleteRequest{Revision: owner.Revision}
	if _, err := service.DeleteCustomer(ctx, "admin", owner.ID, request, "revoke-login"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.DeleteServiceOwner(ctx, "admin", owner.ID, request, "delete-withdrawn-owner"); !errors.Is(err, faults.ErrConflict) {
		t.Fatal("owner deleted before its rule was withdrawn", err)
	}
	if err := f.store.recompileForwardingGroupsLocked([]string{f.rule.EntryGroupID}, f.rule.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	beforeGeneration := f.store.nodes[f.nodeID].DesiredGeneration
	batch := rulegroups.NewService(f.store, nil)
	if _, _, err := batch.BatchForCustomer(ctx, owner.ID, deleteVLESSRequest(f), "withdraw-owned-rule"); err != nil {
		t.Fatal(err)
	}
	if f.store.nodes[f.nodeID].DesiredGeneration != beforeGeneration+1 {
		t.Fatal("rule withdrawal did not publish exactly one generation")
	}
	before, err := f.store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if replayed, err := service.DeleteServiceOwner(ctx, "admin", owner.ID, request, "delete-withdrawn-owner"); err != nil || replayed {
		t.Fatal("service owner delete", replayed, err)
	}
	after, err := f.store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	var left, right map[string]json.RawMessage
	if err := json.Unmarshal(before, &left); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(after, &right); err != nil {
		t.Fatal(err)
	}
	for key, value := range left {
		switch key {
		case "Customers", "CustomerSessions", "AuditEvents", "BusinessIdempotency":
			continue
		}
		if !bytes.Equal(value, right[key]) {
			t.Fatalf("identity cleanup changed protected field %s after rule withdrawal", key)
		}
	}
	restored, err := DecodeSnapshot(after)
	if err != nil {
		t.Fatal("restore withdrawn history without its service owner", err)
	}
	if replayed, err := customers.NewService(restored, nil).DeleteServiceOwner(ctx, "admin", owner.ID, request, "delete-withdrawn-owner"); err != nil || !replayed {
		t.Fatal("restored deletion replay", replayed, err)
	}
	if restored.nodes[f.nodeID].DesiredGeneration != beforeGeneration+1 {
		t.Fatal("service owner deletion or replay published another node generation")
	}
}

func TestServiceOwnerDeletionRejectsReferencesWithoutMutation(t *testing.T) {
	for _, relation := range []string{"login", "revision", "missing", "rule_customer", "rule_owner", "subscription_customer", "subscription_owner", "identity_active", "identity_pending", "identity_unknown", "runtime_active", "runtime_unknown", "cancelled"} {
		t.Run(relation, func(t *testing.T) {
			s, item := customerRepositoryFixture(t)
			item.PasswordHash = nil
			seedCustomer(s, item)
			input := customers.DeleteInput{ID: item.ID, DeletedBy: "admin-test", ExpectedRevision: item.Revision, IdempotencyKey: "delete-service-owner", RequestSHA256: "test-hash"}
			ctx := context.Background()
			want := faults.ErrConflict
			switch relation {
			case "login":
				s.panelAccounts[item.ID] = panelAccount{PasswordHash: []byte("test")}
			case "revision":
				input.ExpectedRevision++
			case "missing":
				input.ID, want = "missing", faults.ErrNotFound
			case "rule_customer":
				s.forwardRules["ref"] = forwarding.Rule{CustomerID: item.ID}
			case "rule_owner":
				s.forwardRules["ref"] = forwarding.Rule{OwnerID: item.ID}
			case "subscription_customer":
				s.subscriptions["ref"] = subscriptions.Record{Item: subscriptions.Item{CustomerID: item.ID}}
			case "subscription_owner":
				s.subscriptions["ref"] = subscriptions.Record{OwnerID: item.ID}
			case "identity_active", "identity_pending", "identity_unknown":
				state := vlessidentity.StateActive
				if relation == "identity_pending" {
					state = vlessidentity.State("pending")
				}
				if relation == "identity_unknown" {
					state = vlessidentity.State("")
				}
				s.vlessBindings["ref"] = vlessidentity.CredentialRecord{Binding: vlessidentity.Binding{CustomerID: item.ID, State: state}}
			case "runtime_active", "runtime_unknown":
				state := vlessruntime.StateActive
				if relation == "runtime_unknown" {
					state = vlessruntime.State("")
				}
				s.vlessRuntimeMaterials["ref"] = vlessruntime.Material{CustomerID: item.ID, State: state}
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want = context.Canceled
			}
			before, err := s.EncodeSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.DeleteServiceOwner(ctx, input, audit.Event{}); !errors.Is(err, want) {
				t.Fatalf("got %v, want %v", err, want)
			}
			after, err := s.EncodeSnapshot()
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("rejection modified state, audit or replay")
			}
		})
	}
}

func TestServiceOwnerDeletionFormat25RestoreReplayAndIsolation(t *testing.T) {
	ctx := context.Background()
	s, item := customerRepositoryFixture(t)
	item.PasswordHash = nil
	seedCustomer(s, item)
	other := item
	other.ID, other.Username = "survivor", "survivor"
	seedCustomer(s, other)
	s.customerSessions["stale-owner"] = customeridentity.Session{CustomerID: item.ID}
	s.customerSessions["survivor"] = customeridentity.Session{CustomerID: other.ID}
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
	before, err := s.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	service := customers.NewService(s, nil)
	request := customers.DeleteRequest{Revision: item.Revision}
	if replayed, err := service.DeleteServiceOwner(ctx, "admin-test", item.ID, request, "delete-service-owner"); err != nil || replayed {
		t.Fatal("delete", replayed, err)
	}
	if len(s.customerSessions) != 1 || s.customerSessions["survivor"].CustomerID != other.ID {
		t.Fatal("session cleanup crossed owner boundary")
	}
	// The stale survivor fixture is removed before restore; legitimate sessions
	// and durable authorization are exercised through the HTTP and PG fixtures.
	delete(s.customerSessions, "survivor")
	after, err := s.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	var left, right map[string]json.RawMessage
	if err := json.Unmarshal(before, &left); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(after, &right); err != nil {
		t.Fatal(err)
	}
	for key, value := range left {
		switch key {
		case "Customers", "CustomerSessions", "AuditEvents", "BusinessIdempotency":
			continue
		}
		if !bytes.Equal(value, right[key]) {
			t.Fatalf("delete changed protected field %s", key)
		}
	}
	if SnapshotVersion != 25 || !SupportsSnapshotVersion(25) {
		t.Fatal("changed format25 support")
	}
	restored, err := DecodeSnapshot(after)
	if err != nil {
		t.Fatal("format25 restore", err)
	}
	if _, err := restored.Customer(ctx, item.ID); !errors.Is(err, faults.ErrNotFound) {
		t.Fatal("owner returned after restore", err)
	}
	if _, err := restored.Customer(ctx, other.ID); err != nil {
		t.Fatal("survivor lost", err)
	}
	if len(restored.auditEvents) != 1 || restored.auditEvents[0].Action != "service_owner.delete" || len(restored.vlessBindings) != 1 || len(restored.vlessRuntimeMaterials) != 1 {
		t.Fatal("audit or revoked history changed")
	}
	replayedBefore, _ := restored.EncodeSnapshot()
	restoredService := customers.NewService(restored, nil)
	if replayed, err := restoredService.DeleteServiceOwner(ctx, "admin-test", item.ID, request, "delete-service-owner"); err != nil || !replayed {
		t.Fatal("durable replay", replayed, err)
	}
	replayedAfter, _ := restored.EncodeSnapshot()
	if !bytes.Equal(replayedBefore, replayedAfter) {
		t.Fatal("replay mutated state")
	}
	for _, input := range []struct {
		id       string
		revision int64
	}{{other.ID, request.Revision}, {item.ID, request.Revision + 1}} {
		if _, err := restoredService.DeleteServiceOwner(ctx, "admin-test", input.id, customers.DeleteRequest{Revision: input.revision}, "delete-service-owner"); !errors.Is(err, faults.ErrIdempotencyConflict) {
			t.Fatal("changed replay accepted", err)
		}
	}
	if _, err := restoredService.DeleteServiceOwner(ctx, "admin-test", item.ID, request, "fresh-service-delete"); !errors.Is(err, faults.ErrNotFound) {
		t.Fatal("missing owner not 404", err)
	}
}
