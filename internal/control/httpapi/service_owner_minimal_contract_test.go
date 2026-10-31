package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
)

// Exercise the integration's actual JSON shape, including omitted fields. A
// service owner needs no panel password, commercial expiry or duplicate quota.
func TestServiceOwnerMinimalVLESSContractAndReplay(t *testing.T) {
	defaults := forwarding.RealityDefaults{ServerName: "reality.example.test", Destination: "reality.example.test:443"}
	f := newBusinessFixture(t, forwarding.WithRealityDefaults(defaults))
	ownerInput := map[string]any{
		"account_kind":  customers.AccountService,
		"username":      "ht_opaque_audit_001",
		"display_name":  "规则展示标签",
		"user_group_id": f.userGroup.ID,
	}
	var owner customers.Customer
	decodeBusinessEntity(t, businessRequest(t, f, http.MethodPost, "/api/v1/customers", "minimal-service-owner", ownerInput, http.StatusCreated), "customer", &owner)
	if owner.ID == "" || owner.AccountKind != customers.AccountService || owner.ExpiresAt != nil || owner.MaxRules != 0 || owner.TrafficLimitBytes != 0 || owner.SpeedLimitMbps != 0 {
		t.Fatal("minimal service owner gained login or commercial constraints")
	}
	stored, err := f.store.Customer(context.Background(), owner.ID)
	if err != nil || len(stored.PasswordHash) != 0 {
		t.Fatal("minimal service owner acquired a panel password", err)
	}
	if _, err := f.store.CustomerIdentityRepository("minimal-contract").CustomerByUsername(context.Background(), owner.Username); !errors.Is(err, faults.ErrNotFound) {
		t.Fatal("service owner was exposed as a panel login", err)
	}
	var ownerReplay struct {
		Customer customers.Customer `json:"customer"`
		Replayed bool               `json:"replayed"`
	}
	decodeResponse(t, businessRequest(t, f, http.MethodPost, "/api/v1/customers", "minimal-service-owner", ownerInput, http.StatusOK), &ownerReplay)
	if !ownerReplay.Replayed || ownerReplay.Customer.ID != owner.ID {
		t.Fatal("minimal owner retry created a second identity")
	}

	ruleInput := map[string]any{
		"name":                  "规则展示标签",
		"entry_group_id":        f.entry.ID,
		"egress_mode":           "DIRECT",
		"ingress_protocol":      "vless_reality",
		"protocol":              "tcp",
		"vless_outbound_mode":   "SOCKS5",
		"vless_socks5_host":     "landing.example.test",
		"vless_socks5_port":     1080,
		"vless_socks5_username": "isolated-upstream-user",
		"vless_socks5_password": "isolated-upstream-secret",
		"selection_policy":      "round_robin",
	}
	path := "/api/v1/customers/" + owner.ID + "/forwarding-rules"
	created := businessRequest(t, f, http.MethodPost, path, "minimal-vless-rule", ruleInput, http.StatusCreated)
	var rule forwarding.Rule
	decodeBusinessEntity(t, created, "rule", &rule)
	if rule.CustomerID != owner.ID || rule.OwnerKind != forwarding.OwnerCustomer || rule.OwnerID != owner.ID || rule.EntryGroupID != f.entry.ID || rule.ListenPort != 20000 {
		t.Fatal("minimal rule lost its technical ownership or automatic port allocation")
	}
	if rule.EffectiveIngressProtocol() != forwarding.IngressVLESSReality || rule.VLESSOutboundMode != forwarding.VLESSOutboundSOCKS5 || rule.Protocol != forwarding.ProtocolTCP || rule.RealityServerName != defaults.ServerName || rule.RealityDestination != defaults.Destination || rule.RealityPublicKey == "" || rule.RealityShortID == "" {
		t.Fatal("minimal rule did not retain VLESS Reality and SOCKS5 runtime inputs")
	}
	var replay struct {
		Rule     forwarding.Rule `json:"rule"`
		Replayed bool            `json:"replayed"`
	}
	decodeResponse(t, businessRequest(t, f, http.MethodPost, path, "minimal-vless-rule", ruleInput, http.StatusOK), &replay)
	if !replay.Replayed || replay.Rule.ID != rule.ID || replay.Rule.ListenPort != rule.ListenPort || replay.Rule.RealityPublicKey != rule.RealityPublicKey || replay.Rule.RealityShortID != rule.RealityShortID {
		t.Fatal("minimal rule retry changed its stable connection material")
	}
	listed := businessRequest(t, f, http.MethodGet, "/api/v1/forwarding-rules/"+rule.ID, "", nil, http.StatusOK)
	audits, err := json.Marshal(f.store.AuditEvents())
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range [][]byte{created, listed, audits} {
		if bytes.Contains(raw, []byte("isolated-upstream-secret")) || bytes.Contains(raw, []byte("reality_private_key")) {
			t.Fatal("rule response or audit exposed private runtime credentials")
		}
	}
}
