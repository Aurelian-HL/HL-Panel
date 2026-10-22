package httpapi_test

import (
	"context"
	"net/http"
	"testing"

	"bytes"
	"encoding/json"
	"errors"
	"github.com/hongle/hl-panel/internal/control/customeridentity"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
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
	businessRequest(t, f, http.MethodGet, path, "", nil, http.StatusOK)
	assertDeletionProblem(t, businessRequest(t, f, http.MethodDelete, path, "delete-customer", customers.DeleteRequest{Revision: request.Revision + 1}, http.StatusConflict), "idempotency_conflict", "")
	businessRequest(t, f, http.MethodDelete, path, "fresh-delete-customer", request, http.StatusNotFound)
}

func TestCustomerDeletionHTTPPreservesExistingRules(t *testing.T) {
	f := newBusinessFixture(t)
	var rule forwarding.Rule
	decodeBusinessEntity(t, businessRequest(t, f, http.MethodPost, "/api/v1/customers/"+f.customer.ID+"/forwarding-rules", "customer-delete-rule", f.ruleRequest(), http.StatusCreated), "rule", &rule)
	before, _ := f.store.EncodeSnapshot()
	path := "/api/v1/customers/" + f.customer.ID
	businessRequest(t, f, http.MethodDelete, path, "customer-delete-retry", customers.DeleteRequest{Revision: f.customer.Revision}, http.StatusOK)
	after, _ := f.store.EncodeSnapshot()
	var left, right map[string]json.RawMessage
	json.Unmarshal(before, &left)
	json.Unmarshal(after, &right)
	for _, key := range []string{"Customers", "ForwardRules", "Subscriptions", "VLESSBindings", "VLESSRuntimeMaterials", "NodeConfigsByNode", "Nodes"} {
		if !bytes.Equal(left[key], right[key]) {
			t.Fatalf("login deletion changed %s", key)
		}
	}
	var listing struct {
		Items []customers.Customer `json:"items"`
	}
	decodeResponse(t, businessRequest(t, f, http.MethodGet, "/api/v1/customers?account_kind=panel", "", nil, http.StatusOK), &listing)
	if len(listing.Items) != 0 {
		t.Fatal("deleted account remains listed")
	}
	if _, err := f.store.CustomerIdentityRepository("test").CustomerByUsername(context.Background(), f.customer.Username); !errors.Is(err, faults.ErrNotFound) {
		t.Fatal("deleted account remains available for login", err)
	}
	businessRequest(t, f, http.MethodGet, "/api/v1/forwarding-rules/"+rule.ID, "", nil, http.StatusOK)
}

func TestLegacyProvisioningIsExcludedFromPanelAccounts(t *testing.T) {
	f := newBusinessFixture(t)
	var owner customers.Customer
	decodeBusinessEntity(t, businessRequest(t, f, http.MethodPost, "/api/v1/customers", "legacy-provision", customers.CustomerInput{Username: "integration-owner", Password: "generated-integration-password"}, http.StatusCreated), "customer", &owner)
	if owner.AccountKind != customers.AccountService {
		t.Fatal("provisioning granted a panel login")
	}
	var listing struct {
		Items []customers.Customer `json:"items"`
	}
	decodeResponse(t, businessRequest(t, f, http.MethodGet, "/api/v1/customers?account_kind=panel", "", nil, http.StatusOK), &listing)
	if len(listing.Items) != 1 || listing.Items[0].ID != f.customer.ID {
		t.Fatal("service owner included in user management")
	}
	if _, err := f.store.CustomerIdentityRepository("test").CustomerByUsername(context.Background(), owner.Username); !errors.Is(err, faults.ErrNotFound) {
		t.Fatal("service owner can log in", err)
	}
}
