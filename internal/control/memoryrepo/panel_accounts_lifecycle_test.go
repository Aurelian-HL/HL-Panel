package memoryrepo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/faults"
)

func TestRemovingPanelLoginPreservesVLESSBundleAndServiceState(t *testing.T) {
	f := newOwnedVLESSBundleFixture(t, true, "cus-1")
	s, ctx := f.store, context.Background()
	beforeConfig, _, err := s.compileNodeConfigLocked(f.nodeID, f.rule.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	owner, err := s.Customer(ctx, f.rule.CustomerID)
	if err != nil || owner.AccountKind != customers.AccountPanel {
		t.Fatal("fixture has no panel login", err)
	}
	if _, err := customers.NewService(s, nil).DeleteCustomer(ctx, "admin", owner.ID, customers.DeleteRequest{Revision: owner.Revision}, "remove-vless-login"); err != nil {
		t.Fatal(err)
	}
	after, err := s.EncodeSnapshot()
	if err != nil || !bytes.Equal(protectedServiceState(t, before), protectedServiceState(t, after)) {
		t.Fatal("removing login changed service data", err)
	}
	for _, store := range []*Store{s, func() *Store {
		restored, err := DecodeSnapshot(after)
		if err != nil {
			t.Fatal(err)
		}
		return restored
	}()} {
		afterConfig, changed, err := store.compileNodeConfigLocked(f.nodeID, f.rule.CreatedAt.Add(time.Minute))
		if err != nil || changed || afterConfig.Generation != beforeConfig.Generation || afterConfig.ConfigSHA256 != beforeConfig.ConfigSHA256 || !bytes.Equal(afterConfig.Config, beforeConfig.Config) {
			t.Fatal("login removal or restart changed VLESS bundle", err)
		}
	}
}

func TestRemovingAllMigratedPanelLoginsKeepsAdministratorAndServiceState(t *testing.T) {
	f := newOwnedVLESSBundleFixture(t, true, "cus-1")
	raw, err := f.store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(legacyLoginSnapshot(t, raw), &doc); err != nil {
		t.Fatal(err)
	}
	doc["Version"] = json.RawMessage(`24`)
	legacy, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	s, err := DecodeSnapshot(legacy)
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.panelAccounts) == 0 {
		t.Fatal("fixture did not migrate any legacy login")
	}
	svc := customers.NewService(s, nil)
	items, err := svc.ListCustomers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, owner := range items {
		if owner.AccountKind != customers.AccountPanel {
			continue
		}
		if _, err := svc.DeleteCustomer(context.Background(), "admin", owner.ID, customers.DeleteRequest{Revision: owner.Revision}, "clear-login-"+owner.ID); err != nil {
			t.Fatal(err)
		}
	}
	after, err := s.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(protectedServiceState(t, before), protectedServiceState(t, after)) {
		t.Fatal("clearing logins changed service or administrator state")
	}
	restored, err := DecodeSnapshot(after)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.panelAccounts) != 0 || len(restored.customerSessions) != 0 || len(restored.adminsByUsername) != len(s.adminsByUsername) {
		t.Fatal("restart restored removed logins or lost administrators")
	}
}

func TestLegacyProvisioningRetryAfterLoginRemovalDoesNotRestoreLogin(t *testing.T) {
	s, _ := customerRepositoryFixture(t)
	svc, ctx := customers.NewService(s, nil), context.Background()
	// Explicit panel creation has the pre-split request fingerprint. Retrying
	// the same historical integration request must reuse its existing owner.
	input := customers.CustomerInput{AccountKind: customers.AccountPanel, Username: "historical-owner", Password: "generated-password", IdempotencyKey: "historical-create"}
	owner, _, err := svc.CreateCustomer(ctx, "admin", input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DeleteCustomer(ctx, "admin", owner.ID, customers.DeleteRequest{Revision: owner.Revision}, "remove-historical-login"); err != nil {
		t.Fatal(err)
	}
	input.AccountKind = ""
	retry, replay, err := svc.CreateCustomer(ctx, "admin", input)
	if err != nil || !replay || retry.ID != owner.ID || retry.AccountKind != customers.AccountService || len(s.panelAccounts) != 0 {
		t.Fatal("legacy retry duplicated an owner or restored its login", err)
	}
}

func TestPanelAccountAndServiceIdentityHaveSeparateLifecycles(t *testing.T) {
	ctx := context.Background()
	s, _ := customerRepositoryFixture(t)
	svc := customers.NewService(s, nil)
	service, _, err := svc.CreateCustomer(ctx, "admin-test", customers.CustomerInput{
		AccountKind: customers.AccountService, Username: "service-only", IdempotencyKey: "service-owner",
	})
	if err != nil {
		t.Fatal(err)
	}
	if service.AccountKind != customers.AccountService || len(s.panelAccounts) != 0 {
		t.Fatal("service owner unexpectedly granted login")
	}
	if _, err := s.CustomerIdentityRepository("test").CustomerByUsername(ctx, service.Username); !errors.Is(err, faults.ErrNotFound) {
		t.Fatal("service identity can authenticate", err)
	}
	input := customers.CustomerInput{AccountKind: customers.AccountPanel, Username: "panel-login", Password: "test-panel-password", MaxRules: 2, SpeedLimitMbps: 10, IdempotencyKey: "panel-login"}
	panel, _, err := svc.CreateCustomer(ctx, "admin-test", input)
	if err != nil {
		t.Fatal(err)
	}
	if panel.AccountKind != customers.AccountPanel || s.customers[panel.ID].AccountKind != "" || len(s.customers[panel.ID].PasswordHash) != 0 {
		t.Fatal("credential split incomplete")
	}
	if _, err := svc.DeleteCustomer(ctx, "admin-test", panel.ID, customers.DeleteRequest{Revision: panel.Revision}, "remove-login"); err != nil {
		t.Fatal(err)
	}
	replayed, replay, err := svc.CreateCustomer(ctx, "admin-test", input)
	if err != nil || !replay || replayed.ID != panel.ID || replayed.AccountKind != customers.AccountService || len(s.panelAccounts) != 0 {
		t.Fatal("create replay resurrected login", err)
	}
	input.Revision, input.IdempotencyKey = panel.Revision, "resurrect-login"
	if _, _, err := svc.UpdateCustomer(ctx, "admin-test", panel.ID, input); !errors.Is(err, faults.ErrValidation) {
		t.Fatal("deleted login resurrected through password edit", err)
	}
	raw, err := s.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.panelAccounts) != 0 || restored.customers[panel.ID].MaxRules != panel.MaxRules || restored.customers[panel.ID].SpeedLimitMbps != panel.SpeedLimitMbps {
		t.Fatal("restore changed service policy or login deletion")
	}
}
