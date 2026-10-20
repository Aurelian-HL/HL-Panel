package httpapi_test

import (
	"bytes"
	"context"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/groupconfig"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
	"github.com/hongle/hl-panel/internal/control/vlesssetup"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

// Verify the formal API sequence using synthetic accounts and a synthetic node.
// Adding a NY-compatible port must not replace the existing customer identity.
func TestCustomerAdditionalPortPreservesExistingIdentityAndPolicy(t *testing.T) {
	f := newBusinessFixture(t)
	ctx := context.Background()
	customerPath := "/api/v1/customers/" + f.customer.ID
	networkPath := "/api/v1/group-networks/" + f.entry.ID
	rulesPath := customerPath + "/forwarding-rules"
	expires := time.Now().UTC().Add(24 * time.Hour)
	customerInput := customers.CustomerInput{
		Username: f.customer.Username, DisplayName: f.customer.DisplayName,
		UserGroupID: f.customer.UserGroupID, MaxRules: 1, TrafficLimitBytes: 1024,
		ExpiresAt: &expires, Revision: f.customer.Revision,
	}
	var customer customers.Customer
	decodeBusinessEntity(t, businessRequest(t, f, http.MethodPut, customerPath, "quota-one", customerInput, http.StatusOK), "customer", &customer)
	beforeCustomer, err := f.store.Customer(ctx, f.customer.ID)
	if err != nil {
		t.Fatal(err)
	}
	networkInput := groupconfig.Request{
		ConnectHost: "entry.example.test", PortStart: 10000, PortEnd: 40156,
		PortRanges:   []groupconfig.PortRange{{Start: 10000, End: 20000}, {Start: 25510, End: 40156}},
		DirectPolicy: groupconfig.DirectPolicyForced, AllowDirect: true,
		AllowedUserGroupIDs: []string{f.userGroup.ID}, TrafficMultiplier: 1.75, Revision: 1,
	}
	var network groupconfig.GroupNetwork
	decodeBusinessEntity(t, businessRequest(t, f, http.MethodPut, networkPath, "two-original-ranges", networkInput, http.StatusOK), "network", &network)
	var issued enrollment.IssueResult
	decodeResponse(t, requestJSON(t, f.handler, http.MethodPost, "/api/v1/enrollment-tokens", f.token,
		map[string]any{"name": "synthetic-entry", "expires_in_seconds": 900}, http.StatusCreated), &issued)
	var enrolled agentv1.EnrollmentResponse
	decodeResponse(t, requestJSON(t, f.handler, http.MethodPost, "/api/v1/agent/enroll", "", agentv1.EnrollmentRequest{
		EnrollmentToken: issued.Token, Hostname: "entry.example.test", Platform: "linux", Architecture: "amd64",
		AgentVersion: "0.1.127", Capabilities: []string{"xray", "vless-reality"},
	}, http.StatusCreated), &enrolled)
	requestJSON(t, f.handler, http.MethodPost, "/api/v1/device-groups/"+f.entry.ID+"/members", f.token,
		map[string]any{"node_id": enrolled.NodeID, "dial_host": "entry.example.test", "weight": 100}, http.StatusOK)
	oldRequest := customerVLESSRequest(f)
	oldRequest.ListenPort = 10021
	var oldRule forwarding.Rule
	decodeBusinessEntity(t, businessRequest(t, f, http.MethodPost, rulesPath, "old-rule", oldRequest, http.StatusCreated), "rule", &oldRule)
	identity, err := vlessidentity.NewService(f.store, time.Now, nil)
	if err != nil {
		t.Fatal(err)
	}
	forwardingService := forwarding.NewService(f.store, time.Now)
	setup := vlesssetup.NewService(groupconfig.NewService(f.store, time.Now), endpoints.NewService(f.store, time.Now), identity, vlesssetup.WithRuleAccess(forwardingService))
	if err := setup.Ensure(ctx, "adm_business", oldRule); err != nil {
		t.Fatal("old setup", err)
	}
	oldCredential, err := identity.CredentialForAdministrator(ctx, "adm_business", oldRule.ID)
	if err != nil {
		t.Fatal(err)
	}
	endpointService := endpoints.NewService(f.store, time.Now)
	oldPool, err := endpointService.FindPoolForRuleForAdministrator(ctx, "adm_business", oldRule.ID)
	if err != nil {
		t.Fatal(err)
	}
	beforeConfig, _, err := f.store.DesiredNodeConfig(ctx, enrolled.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	var beforeUserGroup customers.UserGroup
	decodeBusinessEntity(t, businessRequest(t, f, http.MethodGet, "/api/v1/user-groups/"+f.userGroup.ID, "", nil, http.StatusOK), "user_group", &beforeUserGroup)

	// Append one port, retaining both ranges and every other network field.
	networkInput.PortRanges = append(network.EffectivePortRanges(), groupconfig.PortRange{Start: 60882, End: 60882})
	networkInput.Revision = network.Revision
	decodeBusinessEntity(t, businessRequest(t, f, http.MethodPut, networkPath, "append-single-port", networkInput, http.StatusOK), "network", &network)
	businessRequest(t, f, http.MethodPut, networkPath, "append-single-port", networkInput, http.StatusOK)
	businessRequest(t, f, http.MethodPut, networkPath, "stale-network-cas", networkInput, http.StatusConflict)
	changed := networkInput
	changed.ConnectHost = "different.example.test"
	businessRequest(t, f, http.MethodPut, networkPath, "append-single-port", changed, http.StatusConflict)
	if !reflect.DeepEqual(network.PortRanges, networkInput.PortRanges) || network.ConnectHost != networkInput.ConnectHost ||
		network.TrafficMultiplier != networkInput.TrafficMultiplier || network.EffectiveDirectPolicy() != groupconfig.DirectPolicyForced ||
		!reflect.DeepEqual(network.AllowedUserGroupIDs, networkInput.AllowedUserGroupIDs) {
		t.Fatal("port append altered unrelated network policy")
	}
	afterConfig, _, err := f.store.DesiredNodeConfig(ctx, enrolled.NodeID)
	if err != nil || !reflect.DeepEqual(beforeConfig, afterConfig) {
		t.Fatal("range-only update changed the node configuration", err)
	}
	var afterUserGroup customers.UserGroup
	decodeBusinessEntity(t, businessRequest(t, f, http.MethodGet, "/api/v1/user-groups/"+f.userGroup.ID, "", nil, http.StatusOK), "user_group", &afterUserGroup)
	if !reflect.DeepEqual(beforeUserGroup, afterUserGroup) {
		t.Fatal("entry range update altered the customer user group")
	}
	newRequest := customerVLESSRequest(f)
	newRequest.Name = "additional NY-compatible port"
	newRequest.ListenPort = 60882
	businessRequest(t, f, http.MethodPost, rulesPath, "quota-rejected-port", newRequest, http.StatusConflict)

	// A full customer replacement preserves password and limits; CAS and
	// idempotency prevent replay from raising the quota or duplicating a rule.
	customerInput.Revision = customer.Revision
	customerInput.MaxRules = 2
	decodeBusinessEntity(t, businessRequest(t, f, http.MethodPut, customerPath, "quota-two", customerInput, http.StatusOK), "customer", &customer)
	businessRequest(t, f, http.MethodPut, customerPath, "quota-two", customerInput, http.StatusOK)
	businessRequest(t, f, http.MethodPut, customerPath, "stale-customer-cas", customerInput, http.StatusConflict)
	changedCustomer := customerInput
	changedCustomer.MaxRules = 3
	businessRequest(t, f, http.MethodPut, customerPath, "quota-two", changedCustomer, http.StatusConflict)
	afterCustomer, err := f.store.Customer(ctx, f.customer.ID)
	if err != nil || !bytes.Equal(beforeCustomer.PasswordHash, afterCustomer.PasswordHash) ||
		afterCustomer.MaxRules != 2 || afterCustomer.TrafficLimitBytes != beforeCustomer.TrafficLimitBytes ||
		afterCustomer.SpeedLimitMbps != beforeCustomer.SpeedLimitMbps || afterCustomer.IPLimit != beforeCustomer.IPLimit ||
		afterCustomer.ConnectionLimit != beforeCustomer.ConnectionLimit || afterCustomer.UserGroupID != beforeCustomer.UserGroupID {
		t.Fatal("quota update altered unrelated customer settings", err)
	}
	var newRule forwarding.Rule
	decodeBusinessEntity(t, businessRequest(t, f, http.MethodPost, rulesPath, "new-rule", newRequest, http.StatusCreated), "rule", &newRule)
	businessRequest(t, f, http.MethodPost, rulesPath, "new-rule", newRequest, http.StatusOK)
	if err := setup.Ensure(ctx, "adm_business", newRule); err != nil {
		t.Fatal("new setup", err)
	}
	newCredential, err := identity.CredentialForAdministrator(ctx, "adm_business", newRule.ID)
	if err != nil || newRule.ID == oldRule.ID || newRule.ListenPort != 60882 ||
		newCredential.Binding.ID == oldCredential.Binding.ID || newCredential.CredentialUUID == oldCredential.CredentialUUID {
		t.Fatal("additional port reused the existing rule or identity", err)
	}
	afterOld, err := identity.CredentialForAdministrator(ctx, "adm_business", oldRule.ID)
	if err != nil || !reflect.DeepEqual(oldCredential, afterOld) {
		t.Fatal("additional port changed the old identity", err)
	}
	afterOldPool, err := endpointService.FindPoolForRuleForAdministrator(ctx, "adm_business", oldRule.ID)
	if err != nil || !reflect.DeepEqual(oldPool, afterOldPool) || afterOldPool.Port != 10021 {
		t.Fatal("additional port changed the old endpoint", err)
	}
	storedOld, err := f.store.ForwardingRule(ctx, oldRule.ID)
	if err != nil || storedOld.Revision != oldRule.Revision || storedOld.ListenPort != 10021 || storedOld.Paused {
		t.Fatal("additional port modified the old rule", err)
	}
	bindings, err := identity.List(ctx, "adm_business")
	if err != nil || len(bindings) != 2 {
		t.Fatal("idempotent additional port did not create exactly one identity", err)
	}
}
