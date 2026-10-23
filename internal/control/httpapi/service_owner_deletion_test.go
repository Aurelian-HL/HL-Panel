package httpapi_test

import (
	"context"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/customeridentity"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/rulegroups"
	"github.com/hongle/hl-panel/internal/control/subscriptions"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
)

func TestServiceOwnerDeletionHTTPContract(t *testing.T) {
	f := newBusinessFixture(t)
	var owner customers.Customer
	decodeBusinessEntity(t, businessRequest(t, f, http.MethodPost, "/api/v1/customers", "create-service-owner", customers.CustomerInput{AccountKind: customers.AccountService, Username: "service-delete-test"}, http.StatusCreated), "customer", &owner)
	path := "/api/v1/service-owners/" + owner.ID
	request := customers.DeleteRequest{Revision: owner.Revision}
	requestJSON(t, f.handler, http.MethodDelete, path, "", request, http.StatusUnauthorized)
	identity, err := customeridentity.NewService(f.store.CustomerIdentityRepository("test"), nil, customeridentity.Options{PasswordFingerprintKey: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	login, err := identity.Login(context.Background(), f.customer.Username, "12345678")
	if err != nil {
		t.Fatal(err)
	}
	client := f
	client.token = login.AccessToken
	businessRequest(t, client, http.MethodDelete, path, "delete-service-owner", request, http.StatusUnauthorized)
	assertDeletionProblem(t, businessRequest(t, f, http.MethodDelete, path, "", request, http.StatusBadRequest), "validation_error", "")
	assertDeletionProblem(t, businessRequest(t, f, http.MethodDelete, path, "delete-service-owner", map[string]any{}, http.StatusBadRequest), "validation_error", "")
	assertDeletionProblem(t, businessRequest(t, f, http.MethodDelete, path, "delete-service-owner", customers.DeleteRequest{Revision: owner.Revision + 1}, http.StatusConflict), "conflict", "changed")
	assertDeletionProblem(t, businessRequest(t, f, http.MethodDelete, "/api/v1/service-owners/"+f.customer.ID, "delete-login-owner", customers.DeleteRequest{Revision: f.customer.Revision}, http.StatusConflict), "conflict", "panel login")
	// Old endpoint intentionally does not remove service owners.
	businessRequest(t, f, http.MethodDelete, "/api/v1/customers/"+owner.ID, "old-service-delete", request, http.StatusNotFound)
	before := len(f.store.AuditEvents())
	var result struct {
		Replayed bool `json:"replayed"`
	}
	decodeResponse(t, businessRequest(t, f, http.MethodDelete, path, "delete-service-owner", request, http.StatusOK), &result)
	if result.Replayed {
		t.Fatal("first delete reported replay")
	}
	decodeResponse(t, businessRequest(t, f, http.MethodDelete, path, "delete-service-owner", request, http.StatusOK), &result)
	if !result.Replayed || len(f.store.AuditEvents()) != before+1 {
		t.Fatal("replay changed audit")
	}
	businessRequest(t, f, http.MethodGet, "/api/v1/customers/"+owner.ID, "", nil, http.StatusNotFound)
	businessRequest(t, f, http.MethodGet, "/api/v1/customers/"+f.customer.ID, "", nil, http.StatusOK)
	businessRequest(t, f, http.MethodDelete, path, "fresh-service-delete", request, http.StatusNotFound)
	assertDeletionProblem(t, businessRequest(t, f, http.MethodDelete, path, "delete-service-owner", customers.DeleteRequest{Revision: owner.Revision + 1}, http.StatusConflict), "idempotency_conflict", "")
	if _, err := identity.Authenticate(context.Background(), login.AccessToken); err != nil {
		t.Fatal("another account session affected", err)
	}
}

func TestServiceOwnerDeletionAfterRuleBatchPreservesOtherCustomer(t *testing.T) {
	f := newBusinessFixture(t)
	ctx := context.Background()
	var owner customers.Customer
	decodeBusinessEntity(t, businessRequest(t, f, http.MethodPost, "/api/v1/customers", "create-linked-owner", customers.CustomerInput{
		AccountKind: customers.AccountService, Username: "linked-owner", UserGroupID: f.userGroup.ID,
	}, http.StatusCreated), "customer", &owner)
	createRule := func(customerID, key string) forwarding.Rule {
		t.Helper()
		input := customerVLESSRequest(f)
		input.CustomerID, input.Name = customerID, key
		var rule forwarding.Rule
		decodeBusinessEntity(t, businessRequest(t, f, http.MethodPost, "/api/v1/customers/"+customerID+"/forwarding-rules", key, input, http.StatusCreated), "rule", &rule)
		return rule
	}
	first := createRule(owner.ID, "linked-rule-one")
	second := createRule(owner.ID, "linked-rule-two")
	survivor := createRule(f.customer.ID, "surviving-rule")
	identities, err := vlessidentity.NewService(f.store, time.Now, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Wait for the normal asynchronous provisioning path, rather than seeding
	// credentials directly or bypassing the production deletion handlers.
	for _, rule := range []forwarding.Rule{first, second, survivor} {
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, err = identities.CredentialForAdministrator(ctx, "adm_business", rule.ID); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("identity provisioning", err)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	beforeCredential, err := identities.CredentialForAdministrator(ctx, "adm_business", survivor.ID)
	if err != nil {
		t.Fatal(err)
	}
	var subscription subscriptions.Item
	decodeBusinessEntity(t, businessRequest(t, f, http.MethodPost, "/api/v1/subscriptions", "surviving-subscription", subscriptions.Request{
		Name: "surviving-subscription", CustomerID: f.customer.ID,
		Lines: []subscriptions.Line{{Name: survivor.Name, BindingID: beforeCredential.Binding.ID}},
	}, http.StatusOK), "subscription", &subscription)
	decodeBusinessEntity(t, businessRequest(t, f, http.MethodPost, "/api/v1/subscriptions/"+subscription.ID+"/actions/publish", "publish-surviving-subscription", map[string]any{"revision": subscription.Revision}, http.StatusOK), "subscription", &subscription)
	beforeRule, err := f.store.ForwardingRule(ctx, survivor.ID)
	if err != nil {
		t.Fatal(err)
	}
	beforeSub, err := f.store.Subscription(ctx, "adm_business", subscription.ID)
	if err != nil {
		t.Fatal(err)
	}
	deleteRule := func(rule forwarding.Rule, key string) {
		t.Helper()
		request := rulegroups.BatchRequest{Operation: rulegroups.BatchDelete, RuleIDs: []string{rule.ID}, ExpectedRevisions: map[string]int64{rule.ID: rule.Revision}}
		businessRequest(t, f, http.MethodPost, "/api/v1/forwarding-rules/batch", key, request, http.StatusOK)
		businessRequest(t, f, http.MethodPost, "/api/v1/forwarding-rules/batch", key, request, http.StatusOK)
	}
	ownerPath := "/api/v1/service-owners/" + owner.ID
	request := customers.DeleteRequest{Revision: owner.Revision}
	deleteRule(first, "delete-linked-one")
	businessRequest(t, f, http.MethodDelete, ownerPath, "delete-linked-owner", request, http.StatusConflict)
	businessRequest(t, f, http.MethodGet, "/api/v1/forwarding-rules/"+second.ID, "", nil, http.StatusOK)
	deleteRule(second, "delete-linked-two")
	businessRequest(t, f, http.MethodDelete, ownerPath, "delete-linked-owner", request, http.StatusOK)
	businessRequest(t, f, http.MethodDelete, ownerPath, "delete-linked-owner", request, http.StatusOK)
	businessRequest(t, f, http.MethodGet, "/api/v1/customers/"+owner.ID, "", nil, http.StatusNotFound)
	afterRule, err := f.store.ForwardingRule(ctx, survivor.ID)
	if err != nil || !reflect.DeepEqual(beforeRule, afterRule) {
		t.Fatal("another customer's rule changed", err)
	}
	afterSub, err := f.store.Subscription(ctx, "adm_business", subscription.ID)
	if err != nil || !reflect.DeepEqual(beforeSub, afterSub) {
		t.Fatal("another customer's subscription or token changed", err)
	}
	afterCredential, err := identities.CredentialForAdministrator(ctx, "adm_business", survivor.ID)
	if err != nil || !reflect.DeepEqual(beforeCredential, afterCredential) {
		t.Fatal("another customer's VLESS credential changed", err)
	}
}
