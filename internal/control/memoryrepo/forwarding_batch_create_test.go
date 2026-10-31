package memoryrepo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/control/panelquota"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
	provisioningvless "github.com/hongle/hl-panel/internal/provisioning/vless"
)

func batchCreateFixture(t *testing.T) (*Store, *forwarding.Service, forwarding.BatchCreateRequest) {
	t.Helper()
	s, _ := forwardingRepositoryFixture(t)
	for _, id := range []string{"batch-a", "batch-z"} {
		s.nodes[id] = nodes.Node{ID: id, Name: id, CredentialHash: id + "-credential", Capabilities: []string{"xray", "vless-reality", agentv1.CapabilityUsageGeneration}}
		s.nodesByCredential[id+"-credential"] = id
		s.membersByGroup["entry-1"][id] = groups.Member{GroupID: "entry-1", NodeID: id, DialHost: "entry.example.test", Weight: 1}
	}
	request := forwarding.Request{Name: "", EntryGroupID: "entry-1", EgressMode: forwarding.EgressDirect, IngressProtocol: forwarding.IngressVLESSReality, Protocol: forwarding.ProtocolTCP,
		VLESSFlow: "xtls-rprx-vision", VLESSOutboundMode: forwarding.VLESSOutboundSOCKS5, VLESSSOCKS5Host: "192.0.2.10", VLESSSOCKS5Port: 1080, VLESSSOCKS5Username: "isolated-landing-user", VLESSSOCKS5Password: "isolated-landing-password",
		SelectionPolicy: forwarding.SelectionRoundRobin}
	second := request
	second.Name, second.VLESSSOCKS5Host = "客户昵称", "192.0.2.11"
	return s, forwarding.NewService(s, nil, forwarding.WithRealityTargets(forwarding.DefaultRealityTargets())), forwarding.BatchCreateRequest{Rules: []forwarding.Request{request, second}}
}

func batchSnapshot(t *testing.T, s *Store) []byte {
	t.Helper()
	raw, err := s.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestBatchCreateDefaultNamesPrivateMaterialAndDurableReplay(t *testing.T) {
	s, service, input := batchCreateFixture(t)
	beforeCustomers := len(s.customers)
	result, replayed, err := service.BatchCreateForAdministrator(context.Background(), "admin-test", input, "create-batch")
	if err != nil || replayed || result.Created != 2 || len(result.Items) != 2 {
		t.Fatal("batch creation failed", result.Created, replayed, err)
	}
	if result.Items[0].Name != "192.0.2.10" || result.Items[1].Name != "客户昵称" {
		t.Fatal("optional nickname was not preserved or defaulted")
	}
	if result.Items[0].ListenPort == result.Items[1].ListenPort || result.Items[0].RealityPublicKey == result.Items[1].RealityPublicKey {
		t.Fatal("rules share ports or private material")
	}
	for _, rule := range result.Items {
		upstream, ok := s.VLESSSOCKS5Upstream(rule.ID)
		if !ok || upstream.Password != input.Rules[0].VLESSSOCKS5Password || upstream.Username != input.Rules[0].VLESSSOCKS5Username || s.autoRealityKeys[rule.ID] == "" || s.protocolProbes[rule.ID].UUID == "" {
			t.Fatal("private material was not installed")
		}
		if !rule.OwnedByAdministrator("admin-test") {
			t.Fatal("batch rule was assigned to a customer")
		}
		credential, err := s.CredentialForAdministrator(context.Background(), "admin-test", rule.ID)
		if err != nil || credential.CredentialUUID == s.protocolProbes[rule.ID].UUID {
			t.Fatal("missing independent customer credential", err)
		}
		pool := s.endpointPools[credential.Binding.EndpointPoolID]
		if pool.RuleID != rule.ID || len(s.endpointMembers[pool.ID]) != 2 {
			t.Fatal("stable endpoint membership was not projected")
		}
		if _, err := provisioningvless.URI(vlessProfile(pool, rule, credential.CredentialUUID)); err != nil {
			t.Fatal("batch cannot produce a valid connection URI", err)
		}
	}
	if len(s.customers) != beforeCustomers || len(s.auditEvents) != 1 {
		t.Fatal("batch created customer accounts or duplicate audit events")
	}
	for _, id := range []string{"batch-a", "batch-z"} {
		if s.nodes[id].DesiredGeneration != 1 || len(s.nodeConfigsByNode[id]) != 1 {
			t.Fatal("batch generated redundant node configurations")
		}
		assertBatchListeners(t, s, id, result.Items)
	}
	public, _ := json.Marshal(result)
	event, _ := json.Marshal(s.auditEvents)
	for _, secret := range []string{input.Rules[0].VLESSSOCKS5Password, s.autoRealityKeys[result.Items[0].ID], s.protocolProbes[result.Items[0].ID].UUID} {
		if bytes.Contains(public, []byte(secret)) || bytes.Contains(event, []byte(secret)) {
			t.Fatal("batch response or audit leaked confidential material")
		}
	}
	raw := batchSnapshot(t, s)
	restored, err := DecodeSnapshot(raw)
	if err != nil {
		t.Fatal("batch snapshot cannot be reopened", err)
	}
	restoredService := forwarding.NewService(restored, nil, forwarding.WithRealityTargets(forwarding.DefaultRealityTargets()))
	replay, isReplay, err := restoredService.BatchCreateForAdministrator(context.Background(), "admin-test", input, "create-batch")
	if err != nil || !isReplay || replay.Created != 2 || replay.Items[0].ID != result.Items[0].ID || len(restored.forwardRules) != 2 || len(restored.auditEvents) != 1 {
		t.Fatal("durable retry duplicated the batch", err)
	}
	if restored.autoRealityKeys[result.Items[0].ID] != s.autoRealityKeys[result.Items[0].ID] || restored.nodes["batch-a"].DesiredGeneration != 1 {
		t.Fatal("retry replaced keys or node configurations")
	}
	input.Rules[0].VLESSSOCKS5Password = "changed-isolated-password"
	if _, _, err := restoredService.BatchCreateForAdministrator(context.Background(), "admin-test", input, "create-batch"); !errors.Is(err, faults.ErrIdempotencyConflict) {
		t.Fatal("changed payload did not conflict", err)
	}
}

func assertBatchListeners(t *testing.T, s *Store, nodeID string, rules []forwarding.Rule) {
	t.Helper()
	var bundle agentv1.ConfigurationBundle
	config := s.nodeConfigsByNode[nodeID][s.nodes[nodeID].DesiredGeneration]
	if err := json.Unmarshal(config.Config, &bundle); err != nil || len(bundle.Fragments) != len(rules) {
		t.Fatal("wrong listener count in desired configuration", len(bundle.Fragments), len(rules), err)
	}
	fragments := make(map[string]agentv1.ConfigurationFragment)
	for _, fragment := range bundle.Fragments {
		if fragment.Usage == nil {
			t.Fatal("listener has no rule usage attribution")
		}
		fragments[fragment.Usage.RuleID] = fragment
	}
	for _, rule := range rules {
		fragment, ok := fragments[rule.ID]
		credential, err := s.CredentialForAdministrator(context.Background(), "admin-test", rule.ID)
		if !ok || err != nil || fragment.Engine != agentv1.EngineXray ||
			!bytes.Contains(fragment.Config, []byte(credential.CredentialUUID)) ||
			!bytes.Contains(fragment.Config, []byte(s.autoRealityKeys[rule.ID])) {
			t.Fatal("desired listener lacks customer identity or Reality material", err)
		}
	}
}

func TestBatchCreateFiveHundredRulesCompilesEachNodeOnce(t *testing.T) {
	s, service, input := batchCreateFixture(t)
	network := s.groupNetworks["entry-1"]
	network.PortEnd = network.PortStart + forwarding.MaxImportRules
	s.groupNetworks["entry-1"] = network
	first := input.Rules[0]
	input.Rules = make([]forwarding.Request, forwarding.MaxImportRules)
	for i := range input.Rules {
		input.Rules[i] = first
	}
	started := time.Now()
	result, _, err := service.BatchCreateForAdministrator(context.Background(), "admin-test", input, "full-batch")
	if err != nil || result.Created != forwarding.MaxImportRules {
		t.Fatal("full batch failed", result.Created, err)
	}
	for _, nodeID := range []string{"batch-a", "batch-z"} {
		if s.nodes[nodeID].DesiredGeneration != 1 || len(s.nodeConfigsByNode[nodeID]) != 1 {
			t.Fatal("batch was compiled per rule instead of per node")
		}
		assertBatchListeners(t, s, nodeID, result.Items)
	}
	t.Logf("500 rules / 2 nodes prepared in %s (isolated in-memory measurement)", time.Since(started))
}

func TestBatchCreateFailureLeavesNoPartialRulesOrSecrets(t *testing.T) {
	for _, failure := range []string{"invalid-group", "port-exhausted", "quota", "auto-network-rollback", "compiler"} {
		t.Run(failure, func(t *testing.T) {
			s, service, input := batchCreateFixture(t)
			switch failure {
			case "invalid-group":
				input.Rules[1].EntryGroupID = "missing-entry"
			case "port-exhausted":
				n := s.groupNetworks["entry-1"]
				n.PortEnd = n.PortStart
				s.groupNetworks["entry-1"] = n
			case "quota":
				s.SetPanelQuotaProvider(func() panelquota.Limits { return panelquota.Limits{DeviceGroups: -1, Rules: 1} })
			case "auto-network-rollback":
				delete(s.groupNetworks, "entry-1")
				input.Rules[1].EntryGroupID = "missing-entry"
			case "compiler":
				// Fail the second node after the first has compiled, without making any
				// production request or corrupting an existing forwarding rule.
				s.SetNodeConfigGenerationFloorReader(func(id string) (int64, error) {
					if id == "batch-z" {
						return 0, fmt.Errorf("isolated archive failure")
					}
					return 0, nil
				})
			}
			before := batchSnapshot(t, s)
			if _, _, err := service.BatchCreateForAdministrator(context.Background(), "admin-test", input, "retry-failed-batch"); err == nil {
				t.Fatal("invalid batch succeeded")
			}
			if after := batchSnapshot(t, s); !bytes.Equal(before, after) {
				t.Fatal("failed batch changed durable state")
			}
			if len(s.autoRealityKeys) != 0 || len(s.vlessSOCKS5Upstreams) != 0 || len(s.protocolProbes) != 0 || len(s.forwardRules) != 0 {
				t.Fatal("failed batch retained rules or private material")
			}
			// A failed operation must not reserve its idempotency key.
			input.Rules[1].EntryGroupID = "entry-1"
			n := s.groupNetworks["entry-1"]
			if n.GroupID != "" {
				n.PortEnd = n.PortStart + 9
				s.groupNetworks["entry-1"] = n
			}
			s.SetPanelQuotaProvider(nil)
			s.SetNodeConfigGenerationFloorReader(nil)
			if _, replay, err := service.BatchCreateForAdministrator(context.Background(), "admin-test", input, "retry-failed-batch"); err != nil || replay {
				t.Fatal("failed operation cannot be retried", err)
			}
		})
	}
}

func TestBatchCreateRejectsInvalidItemsBeforeWriting(t *testing.T) {
	s, service, input := batchCreateFixture(t)
	before := batchSnapshot(t, s)
	cases := []struct {
		name string
		edit func(*forwarding.BatchCreateRequest)
		key  string
	}{
		{"empty", func(in *forwarding.BatchCreateRequest) { in.Rules = nil }, "empty"},
		{"over-limit", func(in *forwarding.BatchCreateRequest) {
			in.Rules = make([]forwarding.Request, forwarding.MaxImportRules+1)
		}, "over-limit"},
		{"missing-password", func(in *forwarding.BatchCreateRequest) { in.Rules[1].VLESSSOCKS5Password = "" }, "missing-password"},
		{"explicit-port", func(in *forwarding.BatchCreateRequest) { in.Rules[1].ListenPort = 12005 }, "explicit-port"},
		{"missing-idempotency", func(in *forwarding.BatchCreateRequest) {}, ""},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			in := forwarding.BatchCreateRequest{Rules: append([]forwarding.Request(nil), input.Rules...)}
			tt.edit(&in)
			if _, _, err := service.BatchCreateForAdministrator(context.Background(), "admin-test", in, tt.key); !errors.Is(err, faults.ErrValidation) {
				t.Fatal("invalid batch accepted", err)
			}
			if !bytes.Equal(before, batchSnapshot(t, s)) {
				t.Fatal("invalid batch mutated state")
			}
		})
	}
}

func TestBatchCreateDoesNotModifyExistingRulesOrBindings(t *testing.T) {
	s, service, input := batchCreateFixture(t)
	first, _, err := service.BatchCreateForAdministrator(context.Background(), "admin-test", forwarding.BatchCreateRequest{Rules: input.Rules[1:]}, "existing")
	if err != nil {
		t.Fatal(err)
	}
	existing := first.Items[0]
	beforeRule := cloneForwardingRule(s.forwardRules[existing.ID])
	beforeKey, beforeProbe := s.autoRealityKeys[existing.ID], s.protocolProbes[existing.ID]
	beforeUpstream := s.vlessSOCKS5Upstreams[existing.ID]
	beforeIdentity, err := s.CredentialForAdministrator(context.Background(), "admin-test", existing.ID)
	if err != nil {
		t.Fatal(err)
	}
	beforePool := cloneEndpointPool(s.endpointPools[beforeIdentity.Binding.EndpointPoolID])
	var beforeBundle agentv1.ConfigurationBundle
	if err := json.Unmarshal(s.nodeConfigsByNode["batch-a"][s.nodes["batch-a"].DesiredGeneration].Config, &beforeBundle); err != nil {
		t.Fatal(err)
	}
	result, _, err := service.BatchCreateForAdministrator(context.Background(), "admin-test", input, "new-rules")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.forwardRules[existing.ID], beforeRule) || s.autoRealityKeys[existing.ID] != beforeKey || !reflect.DeepEqual(s.protocolProbes[existing.ID], beforeProbe) || !reflect.DeepEqual(s.vlessSOCKS5Upstreams[existing.ID], beforeUpstream) {
		t.Fatal("batch changed an existing rule or its private material")
	}
	afterIdentity, err := s.CredentialForAdministrator(context.Background(), "admin-test", existing.ID)
	if err != nil || !reflect.DeepEqual(beforeIdentity, afterIdentity) || !reflect.DeepEqual(beforePool, s.endpointPools[beforePool.ID]) {
		t.Fatal("batch replaced an existing identity or stable endpoint", err)
	}
	for _, nodeID := range []string{"batch-a", "batch-z"} {
		assertBatchListeners(t, s, nodeID, append(first.Items, result.Items...))
		var afterBundle agentv1.ConfigurationBundle
		if err := json.Unmarshal(s.nodeConfigsByNode[nodeID][s.nodes[nodeID].DesiredGeneration].Config, &afterBundle); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, fragment := range afterBundle.Fragments {
			if fragment.GroupID == beforeBundle.Fragments[0].GroupID {
				found = true
				if !reflect.DeepEqual(fragment, beforeBundle.Fragments[0]) {
					t.Fatal("batch changed an existing listener fragment")
				}
			}
		}
		if !found {
			t.Fatal("batch removed an existing listener")
		}
	}
	for _, event := range s.auditEvents {
		if strings.Contains(fmt.Sprint(event.Metadata), input.Rules[0].VLESSSOCKS5Password) {
			t.Fatal("audit leaked password")
		}
	}
}
