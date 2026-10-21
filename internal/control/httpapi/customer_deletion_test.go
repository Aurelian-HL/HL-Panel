package httpapi_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/hongle/hl-panel/internal/control/customeridentity"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/rulegroups"
)

func TestCustomerDeletionHTTPAuthorizationValidationAndReplay(t *testing.T) {
	f := newBusinessFixture(t)
	path := "/api/v1/customers/" + f.customer.ID
	request := customers.DeleteRequest{Revision: f.customer.Revision}
	requestJSON(t, f.handler, http.MethodDelete, path, "", request, http.StatusUnauthorized)
	identity, err := customeridentity.NewService(f.store.CustomerIdentityRepository("test"), nil, customeridentity.Options{PasswordFingerprintKey: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	login, err := identity.Login(context.Background(), f.customer.Username, "12345678")
	if err != nil {
		t.Fatal(err)
	}
	customerFixture := f
	customerFixture.token = login.AccessToken
	businessRequest(t, customerFixture, http.MethodDelete, path, "delete-customer", request, http.StatusUnauthorized)
	assertDeletionProblem(t, businessRequest(t, f, http.MethodDelete, path, "", request, http.StatusBadRequest), "validation_error", "")
	assertDeletionProblem(t, businessRequest(t, f, http.MethodDelete, path, "delete-customer", map[string]any{}, http.StatusBadRequest), "validation_error", "")
	assertDeletionProblem(t, businessRequest(t, f, http.MethodDelete, path, "delete-customer", customers.DeleteRequest{Revision: request.Revision + 1}, http.StatusConflict), "conflict", "changed")
	before := len(f.store.AuditEvents())
	var result struct {
		Replayed bool `json:"replayed"`
	}
	decodeResponse(t, businessRequest(t, f, http.MethodDelete, path, "delete-customer", request, http.StatusOK), &result)
	if result.Replayed {
		t.Fatal("first successful deletion reported a replay")
	}
	decodeResponse(t, businessRequest(t, f, http.MethodDelete, path, "delete-customer", request, http.StatusOK), &result)
	if !result.Replayed || len(f.store.AuditEvents()) != before+1 {
		t.Fatal("deletion replay appended audit or was not replayed")
	}
	businessRequest(t, f, http.MethodGet, path, "", nil, http.StatusNotFound)
	assertDeletionProblem(t, businessRequest(t, f, http.MethodDelete, path, "delete-customer", customers.DeleteRequest{Revision: request.Revision + 1}, http.StatusConflict), "idempotency_conflict", "")
	businessRequest(t, f, http.MethodDelete, path, "fresh-delete-customer", request, http.StatusNotFound)
}

func TestCustomerDeletionHTTPPreservesCustomerUntilRulesRemoved(t *testing.T) {
	f := newBusinessFixture(t)
	var rule forwarding.Rule
	decodeBusinessEntity(t, businessRequest(t, f, http.MethodPost, "/api/v1/customers/"+f.customer.ID+"/forwarding-rules", "customer-delete-rule", f.ruleRequest(), http.StatusCreated), "rule", &rule)
	path := "/api/v1/customers/" + f.customer.ID
	request := customers.DeleteRequest{Revision: f.customer.Revision}
	assertDeletionProblem(t, businessRequest(t, f, http.MethodDelete, path, "customer-delete-retry", request, http.StatusConflict), "conflict", "forwarding rules")
	businessRequest(t, f, http.MethodGet, path, "", nil, http.StatusOK)
	businessRequest(t, f, http.MethodPost, "/api/v1/forwarding-rules/batch", "delete-customer-rule", rulegroups.BatchRequest{Operation: rulegroups.BatchDelete, RuleIDs: []string{rule.ID}, ExpectedRevisions: map[string]int64{rule.ID: rule.Revision}}, http.StatusOK)
	// A failed attempt must not consume the deletion key.
	businessRequest(t, f, http.MethodDelete, path, "customer-delete-retry", request, http.StatusOK)
}
