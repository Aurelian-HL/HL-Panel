package httpapi_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
)

func TestAdministratorCustomerForwardingOwnershipAndReplay(t *testing.T) {
	f := newBusinessFixture(t)
	path := "/api/v1/customers/" + f.customer.ID + "/forwarding-rules"
	input := f.ruleRequest()
	input.CustomerID = "body-cannot-override-path"
	requestJSON(t, f.handler, http.MethodPost, path, "", input, http.StatusUnauthorized)
	var first forwarding.Rule
	decodeBusinessEntity(t, businessRequest(t, f, http.MethodPost, path, "administrator-owned-rule", input, http.StatusCreated), "rule", &first)
	if first.CustomerID != f.customer.ID || first.OwnerKind != forwarding.OwnerCustomer || first.OwnerID != f.customer.ID {
		t.Fatalf("wrong ownership: %+v", first)
	}
	service := forwarding.NewService(f.store, nil)
	if _, err := service.GetForAdministrator(context.Background(), "adm_business", first.ID); err != nil {
		t.Fatalf("creating administrator cannot read delegated rule: %v", err)
	}
	if _, err := service.GetForAdministrator(context.Background(), "adm_other", first.ID); !errors.Is(err, faults.ErrNotFound) {
		t.Fatal("other administrator gained delegated access")
	}
	raw, err := f.store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := memoryrepo.DecodeSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	restoredService := forwarding.NewService(restored, nil)
	if _, err := restoredService.GetForAdministrator(context.Background(), "adm_business", first.ID); err != nil {
		t.Fatalf("delegated access lost on restart: %v", err)
	}
	legacyReplay, replayed, err := restoredService.CreateForCustomerByAdministrator(context.Background(), "adm_business", f.customer.ID, f.ruleRequest(), "administrator-owned-rule")
	if err != nil || !replayed || legacyReplay.ID != first.ID {
		t.Fatalf("old creation namespace did not replay: %v", err)
	}
	events := f.store.AuditEvents()
	count := len(events)
	var replay forwarding.Rule
	decodeBusinessEntity(t, businessRequest(t, f, http.MethodPost, path, "administrator-owned-rule", input, http.StatusOK), "rule", &replay)
	if replay.ID != first.ID || replay.ListenPort != first.ListenPort {
		t.Fatal("replay allocated another rule")
	}
	events = f.store.AuditEvents()
	if len(events) != count {
		t.Fatal("replay wrote another audit event")
	}
	event := events[len(events)-1]
	if event.ActorType != "administrator" || event.ActorID != "adm_business" {
		t.Fatalf("wrong audit actor: %+v", event)
	}
	businessRequest(t, f, http.MethodPost, path, "administrator-rule-two", f.ruleRequest(), http.StatusCreated)
	businessRequest(t, f, http.MethodPost, path, "administrator-rule-three", f.ruleRequest(), http.StatusCreated)
	businessRequest(t, f, http.MethodPost, path, "administrator-rule-four", f.ruleRequest(), http.StatusConflict)
	bad := f.ruleRequest()
	bad.EntryGroupID = "unauthorized-entry"
	businessRequest(t, f, http.MethodPost, path, "administrator-bad-entry", bad, http.StatusBadRequest)
	businessRequest(t, f, http.MethodPost, "/api/v1/customers/missing/forwarding-rules", "administrator-missing", f.ruleRequest(), http.StatusBadRequest)
}

func TestCustomerAuthoredRulesDoNotGrantAdministratorAccess(t *testing.T) {
	f := newBusinessFixture(t)
	s := forwarding.NewService(f.store, nil)
	rule, _, err := s.CreateForCustomer(context.Background(), f.customer.ID, f.ruleRequest(), "customer-only")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetForAdministrator(context.Background(), "adm_business", rule.ID); !errors.Is(err, faults.ErrNotFound) {
		t.Fatal("customer-authored rule exposed to administrator")
	}
}
