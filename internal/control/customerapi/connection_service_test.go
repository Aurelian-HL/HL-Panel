package customerapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/customeridentity"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
	provisioningvless "github.com/hongle/hl-panel/internal/provisioning/vless"
)

type connectionReaderFunc func(context.Context, string) ([]ConnectionEvidence, error)

func (fn connectionReaderFunc) ListConnectionEvidence(ctx context.Context, customerID string) ([]ConnectionEvidence, error) {
	return fn(ctx, customerID)
}

func testConnectionHandler(t *testing.T, now time.Time, reader ConnectionReader) http.Handler {
	t.Helper()
	identity, err := customeridentity.NewService(newAPIRepository(t, now), func() time.Time { return now }, customeridentity.Options{
		SessionTTL: time.Hour, PasswordFingerprintKey: []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return New(identity, nil, WithConnectionReader(reader))
}

func connectionViews(t *testing.T, body []byte) []ConnectionView {
	t.Helper()
	var response struct {
		Items []ConnectionView `json:"items"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatal(err)
	}
	return response.Items
}

func validConnectionEvidence(t *testing.T, now time.Time) ConnectionEvidence {
	t.Helper()
	publicKey, _, err := provisioningvless.GenerateRealityKeyPair(bytes.NewReader(bytes.Repeat([]byte{7}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	observed := now.Add(-time.Second)
	return ConnectionEvidence{
		Rule: forwarding.Rule{
			ID: "rule_api", Name: "直连", CustomerID: "cus_api", EntryGroupID: "group_api",
			IngressProtocol: forwarding.IngressVLESSReality, Protocol: forwarding.ProtocolTCP,
			VLESSFlow: "xtls-rprx-vision", RealityServerName: "www.example.com",
			VLESSOutboundMode: forwarding.VLESSOutboundSOCKS5, VLESSSOCKS5Host: "landing.example.test", VLESSSOCKS5Port: 1080,
			VLESSSOCKS5Username: "landing-user",
			RealityPublicKey: publicKey, RealityShortID: "a1b2", RealityDestination: "www.example.com:443",
			ListenPort: 443, Revision: 3, Status: forwarding.StatusPendingActivation, Deployed: true,
		},
		Pool: endpoints.EndpointPool{ID: "pool_api", RuleID: "rule_api", GroupID: "group_api",
			Mode: endpoints.ModeSingleServiceEndpoint, Protocol: "vless", Hostname: "entry.example.com", Port: 443, Name: "统一入口"},
		Binding: vlessidentity.CredentialRecord{
			Binding: vlessidentity.Binding{ID: "binding_api", CustomerID: "cus_api", ForwardingRuleID: "rule_api",
				EndpointPoolID: "pool_api", State: vlessidentity.StateActive},
			CredentialUUID: "11111111-1111-4111-8111-111111111111",
		},
		ActiveBindingCount: 1, ActivePoolCount: 1,
		Members: []ConnectionMemberEvidence{{
			Member: endpoints.EndpointPoolMember{PoolID: "pool_api", GroupID: "group_api", NodeID: "node_api",
				Weight: 1, State: endpoints.CandidateEligible, LastHealthAt: &observed},
			DeploymentReceiptVerified: true, DeployedRuleRevision: 3, ProtocolHealthExpiresAt: now.Add(time.Minute),
		}},
		Gateway: GatewayEvidence{Published: true, PublishedRuleRevision: 3, CandidateNodeIDs: []string{"node_api"}},
	}
}

func TestCustomerConnectionsDefaultFailsClosed(t *testing.T) {
	now := time.Date(2026, 10, 3, 2, 0, 0, 0, time.UTC)
	handler := newAPIHandler(t, newAPIRepository(t, now), now)
	unauthorized := request(t, handler, http.MethodGet, "/api/v1/customer/connections", "", nil, nil)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}
	token := loginAPI(t, handler)
	response := request(t, handler, http.MethodGet, "/api/v1/customer/connections?customer_id=cus_other", token, nil, nil)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("response = %d %#v", response.Code, response.Header())
	}
	views := connectionViews(t, response.Body.Bytes())
	if len(views) != 1 || views[0].RuleID != "rule_api" || views[0].Status != "unavailable" || views[0].URI != "" || views[0].Endpoint != "" ||
		strings.Contains(response.Body.String(), "cus_other") || strings.Contains(response.Body.String(), "vless://") {
		t.Fatalf("unverified connection projection is unsafe: %s", response.Body.String())
	}
}

func TestCustomerConnectionURIRequiresAllAuthoritativeEvidence(t *testing.T) {
	now := time.Date(2026, 10, 3, 2, 0, 0, 0, time.UTC)
	base := validConnectionEvidence(t, now)
	tests := []struct {
		name string
		edit func(*ConnectionEvidence)
	}{
		{"not deployed", func(e *ConnectionEvidence) { e.Rule.Deployed = false }},
		{"paused", func(e *ConnectionEvidence) { e.Rule.Paused = true }},
		{"duplicate binding", func(e *ConnectionEvidence) { e.ActiveBindingCount = 2 }},
		{"wrong binding owner", func(e *ConnectionEvidence) { e.Binding.Binding.CustomerID = "cus_other" }},
		{"missing Reality parameter", func(e *ConnectionEvidence) { e.Rule.RealityShortID = "" }},
		{"wrong pool rule", func(e *ConnectionEvidence) { e.Pool.RuleID = "another" }},
		{"missing deployment receipt", func(e *ConnectionEvidence) { e.Members[0].DeploymentReceiptVerified = false }},
		{"stale deployment", func(e *ConnectionEvidence) { e.Members[0].DeployedRuleRevision = 2 }},
		{"expired protocol health", func(e *ConnectionEvidence) { e.Members[0].ProtocolHealthExpiresAt = now }},
		{"gateway not published", func(e *ConnectionEvidence) { e.Gateway.Published = false }},
		{"gateway member mismatch", func(e *ConnectionEvidence) { e.Gateway.CandidateNodeIDs = []string{"another"} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evidence := base
			evidence.Members = append([]ConnectionMemberEvidence(nil), base.Members...)
			test.edit(&evidence)
			service := NewConnectionService(nil, nil, func() time.Time { return now })
			view := ConnectionView{Status: "unavailable"}
			service.populate(&view, customeridentity.Profile{ID: "cus_api", EffectiveStatus: "active"}, evidence)
			if view.URI != "" || view.Status != "unavailable" {
				t.Fatalf("unsafe URI issued: %+v", view)
			}
		})
	}
	view := ConnectionView{Status: "unavailable"}
	NewConnectionService(nil, nil, func() time.Time { return now }).populate(&view, customeridentity.Profile{ID: "cus_api", EffectiveStatus: "active"}, base)
	if view.Status != "ready" || !strings.HasPrefix(view.URI, "vless://11111111-1111-4111-8111-111111111111@entry.example.com:443?") ||
		!strings.Contains(view.URI, "security=reality") || !strings.Contains(view.URI, "flow=xtls-rprx-vision") {
		t.Fatalf("valid connection did not produce one Reality Vision URI: %+v", view)
	}
}

func TestCustomerConnectionReaderIsCustomerScopedAndUnique(t *testing.T) {
	now := time.Date(2026, 10, 3, 2, 0, 0, 0, time.UTC)
	evidence := validConnectionEvidence(t, now)
	reader := connectionReaderFunc(func(_ context.Context, customerID string) ([]ConnectionEvidence, error) {
		if customerID != "cus_api" {
			t.Fatalf("reader received a foreign customer: %q", customerID)
		}
		return []ConnectionEvidence{evidence, evidence}, nil
	})
	handler := testConnectionHandler(t, now, reader)
	token := loginAPI(t, handler)
	response := request(t, handler, http.MethodGet, "/api/v1/customer/connections?customer_id=cus_other", token, nil, nil)
	views := connectionViews(t, response.Body.Bytes())
	if response.Code != http.StatusOK || len(views) != 1 || views[0].URI != "" || views[0].Status != "unavailable" {
		t.Fatalf("duplicate rule evidence escaped: %s", response.Body.String())
	}
	evidence.Rule.CustomerID = "cus_other"
	response = request(t, handler, http.MethodGet, "/api/v1/customer/connections", token, nil, nil)
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), "cus_other") {
		t.Fatalf("foreign evidence escaped: %d %s", response.Code, response.Body.String())
	}
}
