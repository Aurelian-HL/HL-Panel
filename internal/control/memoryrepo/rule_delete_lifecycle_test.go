package memoryrepo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/rulegroups"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
	"github.com/hongle/hl-panel/internal/control/vlessruntime"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func deleteVLESSRequest(f vlessBundleFixture) rulegroups.BatchRequest {
	return rulegroups.BatchRequest{Operation: rulegroups.BatchDelete, RuleIDs: []string{f.rule.ID}, ExpectedRevisions: map[string]int64{f.rule.ID: f.rule.Revision}}
}

func TestBatchDeleteVLESSLifecycleWithdrawsOwnedMaterialsAtomically(t *testing.T) {
	f := newVLESSBundleFixture(t, true)
	ctx := context.Background()
	now := f.rule.UpdatedAt.Add(time.Hour)
	otherNode := f.store.nodes[f.nodeID]
	otherNode.ID, otherNode.Name, otherNode.CredentialHash = "vless-node-second", "vless-node-second", strings.Repeat("w", 64)
	f.store.nodes[otherNode.ID] = otherNode
	f.store.nodesByCredential[otherNode.CredentialHash] = otherNode.ID
	f.store.membersByGroup[f.rule.EntryGroupID][otherNode.ID] = groups.Member{GroupID: f.rule.EntryGroupID, NodeID: otherNode.ID, Weight: 1, CreatedAt: f.rule.CreatedAt, UpdatedAt: f.rule.CreatedAt}
	f.store.endpointMembers[f.endpointID][otherNode.ID] = endpoints.EndpointPoolMember{PoolID: f.endpointID, GroupID: f.rule.EntryGroupID, NodeID: otherNode.ID, Weight: 1, State: endpoints.CandidateEligible, UpdatedAt: f.rule.CreatedAt}
	second := f.store.vlessRuntimeMaterials[f.materialID]
	second.ID, second.NodeID = "material-vless-second", otherNode.ID
	f.store.vlessRuntimeMaterials[second.ID] = second
	if err := f.store.recompileForwardingGroupsLocked([]string{f.rule.EntryGroupID}, f.rule.CreatedAt); err != nil {
		t.Fatal(err)
	}
	beforeGeneration := map[string]int64{}
	for _, nodeID := range []string{f.nodeID, otherNode.ID} {
		beforeGeneration[nodeID] = f.store.nodes[nodeID].DesiredGeneration
	}
	request := deleteVLESSRequest(f)
	service := rulegroups.NewService(f.store, func() time.Time { return now })
	result, replayed, err := service.BatchForAdministrator(ctx, "admin", request, "delete-material-lifecycle")
	if err != nil || replayed || len(result.Items) != 1 {
		t.Fatalf("deleting a rule with its own runtime must succeed atomically: replay=%v err=%v", replayed, err)
	}
	for _, id := range []string{f.materialID, second.ID} {
		material := f.store.vlessRuntimeMaterials[id]
		if material.State != vlessruntime.StateRevoked || material.Revision != 2 || material.RevokedAt == nil || material.CredentialUUID != "" || material.RealityPrivateKey != "" || len(material.NativeConfig) != 0 {
			t.Fatalf("runtime was not revoked and cleared: id=%s state=%s revision=%d", id, material.State, material.Revision)
		}
	}
	if _, err := f.store.ForwardingRule(ctx, f.rule.ID); !errors.Is(err, faults.ErrNotFound) {
		t.Fatal("deleted rule still exists")
	}
	if _, err := f.store.EndpointPool(ctx, f.endpointID); !errors.Is(err, faults.ErrNotFound) {
		t.Fatal("deleted rule endpoint still exists")
	}
	binding := f.store.vlessBindings[f.bindingID]
	if binding.Binding.State != vlessidentity.StateRevoked || binding.CredentialUUID != "" {
		t.Fatal("deleted rule identity still accepts credentials")
	}
	for _, nodeID := range []string{f.nodeID, otherNode.ID} {
		generation := f.store.nodes[nodeID].DesiredGeneration
		if generation != beforeGeneration[nodeID]+1 {
			t.Fatalf("node %s did not receive exactly one deletion generation", nodeID)
		}
		var bundle agentv1.ConfigurationBundle
		if err := json.Unmarshal(f.store.nodeConfigsByNode[nodeID][generation].Config, &bundle); err != nil {
			t.Fatal(err)
		}
		for _, fragment := range bundle.Fragments {
			if fragment.GroupID == generations.ForwardingFragmentID("xray", f.rule.ID) {
				t.Fatal("deleted listener remains in a node's desired bundle")
			}
		}
	}
	runtimeAudits, identityAudits := 0, 0
	for _, event := range f.store.AuditEvents() {
		if event.Action == "vless_runtime.revoke" {
			runtimeAudits++
		}
		if event.Action == "vless_identity.revoke" {
			identityAudits++
		}
	}
	if runtimeAudits != 2 || identityAudits != 1 {
		t.Fatalf("unexpected revocation audit counts: runtime=%d identity=%d", runtimeAudits, identityAudits)
	}
	public, err := json.Marshal(struct {
		Result any
		Audits any
	}{result, f.store.AuditEvents()})
	if err != nil || bytes.Contains(public, []byte(f.credential)) || bytes.Contains(public, []byte(f.privateKey)) {
		t.Fatal("deletion response or audit leaked a credential")
	}
	raw, err := f.store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeSnapshot(raw)
	if err != nil {
		t.Fatalf("deletion history cannot restore: %v", err)
	}
	for _, store := range []*Store{f.store, restored} {
		before := len(store.AuditEvents())
		_, replayed, err := rulegroups.NewService(store, func() time.Time { return now }).BatchForAdministrator(ctx, "admin", request, "delete-material-lifecycle")
		if err != nil || !replayed || len(store.AuditEvents()) != before {
			t.Fatalf("deletion replay changed state after persistence: replay=%v err=%v", replayed, err)
		}
	}
}

func TestBatchDeleteVLESSLifecyclePreservesConnectionOwnershipAndRevisionGuards(t *testing.T) {
	for _, test := range []string{"active connections", "invalid connection count", "foreign pool owner", "material outside own pool", "identity outside own pool", "inconsistent material binding", "foreign material rule", "foreign material customer", "foreign material binding", "foreign binding rule", "runtime revision overflow", "identity revision overflow", "wrong administrator", "stale rule"} {
		t.Run(test, func(t *testing.T) {
			f := newVLESSBundleFixture(t, true)
			actor := "admin"
			request := deleteVLESSRequest(f)
			material := f.store.vlessRuntimeMaterials[f.materialID]
			binding := f.store.vlessBindings[f.bindingID]
			switch test {
			case "active connections":
				member := f.store.endpointMembers[f.endpointID][f.nodeID]
				member.ActiveConnections = 1
				f.store.endpointMembers[f.endpointID][f.nodeID] = member
			case "invalid connection count":
				member := f.store.endpointMembers[f.endpointID][f.nodeID]
				member.ActiveConnections = -1
				f.store.endpointMembers[f.endpointID][f.nodeID] = member
			case "foreign pool owner":
				pool := f.store.endpointPools[f.endpointID]
				pool.OwnerID = "another-admin"
				f.store.endpointPools[f.endpointID] = pool
			case "material outside own pool":
				material.EndpointPoolID = "another-pool"
			case "identity outside own pool":
				binding.Binding.EndpointPoolID = "another-pool"
			case "inconsistent material binding":
				material.ForwardingRuleID, material.EndpointPoolID = "another-rule", "another-pool"
			case "foreign material rule":
				material.ForwardingRuleID = "another-rule"
			case "foreign material customer":
				material.CustomerID = "another-customer"
			case "foreign material binding":
				material.BindingID = "another-binding"
			case "foreign binding rule":
				binding.Binding.ForwardingRuleID = "another-rule"
			case "runtime revision overflow":
				material.Revision = math.MaxInt64
			case "identity revision overflow":
				binding.Binding.Revision = math.MaxInt64
			case "wrong administrator":
				actor = "another-admin"
			case "stale rule":
				request.ExpectedRevisions[f.rule.ID]++
			}
			f.store.vlessRuntimeMaterials[f.materialID] = material
			f.store.vlessBindings[f.bindingID] = binding
			before, err := f.store.EncodeSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = rulegroups.NewService(f.store, nil).BatchForAdministrator(context.Background(), actor, request, "delete-guard")
			want := faults.ErrConflict
			if test == "wrong administrator" {
				want = faults.ErrNotFound
			}
			if !errors.Is(err, want) {
				t.Fatalf("deletion guard failed: got=%v want=%v", err, want)
			}
			after, err := f.store.EncodeSnapshot()
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("a rejected deletion changed durable state, audits, or generations")
			}
		})
	}
}

func TestBatchDeleteVLESSLifecyclePreservesOtherRulesForSharedCustomer(t *testing.T) {
	f := newVLESSBundleFixture(t, true)
	other := cloneForwardingRule(f.rule)
	other.ID, other.Name, other.ListenPort = "shared-customer-other-rule", "other rule", f.rule.ListenPort+1
	f.store.forwardRules[other.ID] = other
	pool := f.store.endpointPools[f.endpointID]
	pool.ID, pool.RuleID, pool.Port = "shared-customer-other-pool", other.ID, other.ListenPort
	f.store.endpointPools[pool.ID] = pool
	member := f.store.endpointMembers[f.endpointID][f.nodeID]
	member.PoolID = pool.ID
	f.store.endpointMembers[pool.ID] = map[string]endpoints.EndpointPoolMember{f.nodeID: member}
	binding := cloneVLESSCredentialRecord(f.store.vlessBindings[f.bindingID])
	binding.Binding.ID, binding.Binding.ForwardingRuleID, binding.Binding.EndpointPoolID = "shared-customer-other-binding", other.ID, pool.ID
	f.store.vlessBindings[binding.Binding.ID] = binding
	material := cloneVLESSRuntimeMaterial(f.store.vlessRuntimeMaterials[f.materialID])
	material.ID, material.ForwardingRuleID, material.EndpointPoolID, material.BindingID = "shared-customer-other-material", other.ID, pool.ID, binding.Binding.ID
	f.store.vlessRuntimeMaterials[material.ID] = material
	f.store.vlessSOCKS5Upstreams[other.ID] = f.store.vlessSOCKS5Upstreams[f.rule.ID]
	if _, _, err := rulegroups.NewService(f.store, nil).BatchForAdministrator(context.Background(), "admin", deleteVLESSRequest(f), "delete-one-shared-customer-rule"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(other, f.store.forwardRules[other.ID]) || !reflect.DeepEqual(pool, f.store.endpointPools[pool.ID]) ||
		!reflect.DeepEqual(binding, f.store.vlessBindings[binding.Binding.ID]) || !reflect.DeepEqual(material, f.store.vlessRuntimeMaterials[material.ID]) {
		t.Fatal("deleting one rule changed another rule owned by the same customer")
	}
	if _, err := f.store.RuntimeMaterialForNode(context.Background(), f.nodeID, binding.Binding.ID); err != nil {
		t.Fatal("another rule no longer has usable runtime material", err)
	}
}

func TestBatchDeleteVLESSLifecycleNativeSecretsRollbackAndWithdrawal(t *testing.T) {
	f, profile := nativeRuntimeFixture(t)
	ctx := context.Background()
	service, err := vlessruntime.NewService(f.store, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	material, _, err := service.BindImported(ctx, "admin", f.bindingID, f.nodeID, profile.Config, "delete-native-bind")
	if err != nil {
		t.Fatal(err)
	}
	broken := cloneForwardingRule(f.rule)
	broken.ID, broken.IngressProtocol, broken.Protocol, broken.ListenPort, broken.Paused = "broken-rule", forwarding.IngressTCP, forwarding.ProtocolTCP, 12009, false
	broken.Targets = []forwarding.Target{{Host: "", Port: 9000}}
	f.store.forwardRules[broken.ID] = broken
	before := cloneVLESSRuntimeMaterial(f.store.vlessRuntimeMaterials[material.ID])
	request := deleteVLESSRequest(f)
	deletion := rulegroups.NewService(f.store, nil)
	if _, _, err := deletion.BatchForAdministrator(ctx, "admin", request, "delete-native-rule"); err == nil {
		t.Fatal("expected an isolated compiler failure")
	}
	if !reflect.DeepEqual(before, f.store.vlessRuntimeMaterials[material.ID]) {
		t.Fatal("failed deletion did not restore the native runtime secrets")
	}
	delete(f.store.forwardRules, broken.ID)
	if _, _, err := deletion.BatchForAdministrator(ctx, "admin", request, "delete-native-rule"); err != nil {
		t.Fatal(err)
	}
	after := f.store.vlessRuntimeMaterials[material.ID]
	if after.State != vlessruntime.StateRevoked || len(after.NativeConfig) != 0 || after.CredentialUUID != "" || after.RealityPrivateKey != "" {
		t.Fatal("native runtime retained secret material after deletion")
	}
	raw, err := f.store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeSnapshot(raw); err != nil {
		t.Fatal("native deletion history cannot be restored", err)
	}
}

func TestBatchDeleteVLESSLifecycleCompileFailureRestoresAllState(t *testing.T) {
	f := newVLESSBundleFixture(t, true)
	ctx := context.Background()
	now := f.rule.UpdatedAt.Add(time.Hour)
	if err := f.store.recompileForwardingGroupsLocked([]string{f.rule.EntryGroupID}, now); err != nil {
		t.Fatal(err)
	}
	broken := cloneForwardingRule(f.rule)
	broken.ID, broken.IngressProtocol, broken.Protocol, broken.ListenPort = "broken-other-rule", forwarding.IngressTCP, forwarding.ProtocolTCP, 12009
	broken.Targets = []forwarding.Target{{Host: "", Port: 9000}}
	f.store.forwardRules[broken.ID] = broken
	beforeRule := f.store.forwardRules[f.rule.ID]
	beforeMaterial := f.store.vlessRuntimeMaterials[f.materialID]
	beforeBinding := f.store.vlessBindings[f.bindingID]
	beforePool := f.store.endpointPools[f.endpointID]
	beforeMembers := f.store.endpointMembers[f.endpointID]
	beforeNode := f.store.nodes[f.nodeID]
	beforeGeneration := len(f.store.nodeConfigsByNode[f.nodeID])
	beforeAudits := len(f.store.AuditEvents())
	service := rulegroups.NewService(f.store, func() time.Time { return now })
	request := deleteVLESSRequest(f)
	_, _, err := service.BatchForAdministrator(ctx, "admin", request, "delete-compile-rollback")
	if err == nil || !strings.Contains(err.Error(), "recompile node") {
		t.Fatalf("expected compilation failure, got %v", err)
	}
	if !reflect.DeepEqual(f.store.forwardRules[f.rule.ID], beforeRule) || !reflect.DeepEqual(f.store.vlessRuntimeMaterials[f.materialID], beforeMaterial) || !reflect.DeepEqual(f.store.vlessBindings[f.bindingID], beforeBinding) || !reflect.DeepEqual(f.store.endpointPools[f.endpointID], beforePool) || !reflect.DeepEqual(f.store.endpointMembers[f.endpointID], beforeMembers) || !reflect.DeepEqual(f.store.nodes[f.nodeID], beforeNode) || len(f.store.nodeConfigsByNode[f.nodeID]) != beforeGeneration || len(f.store.AuditEvents()) != beforeAudits {
		t.Fatal("failed deletion did not restore all related state")
	}
	delete(f.store.forwardRules, broken.ID)
	_, replayed, err := service.BatchForAdministrator(ctx, "admin", request, "delete-compile-rollback")
	if err != nil || replayed {
		t.Fatalf("failed deletion consumed its idempotency key: replay=%v err=%v", replayed, err)
	}
}
