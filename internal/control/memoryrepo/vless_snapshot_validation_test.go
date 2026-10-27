package memoryrepo

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/vlessruntime"
)

func TestVLESSRealitySnapshotRoundTripAndValidation(t *testing.T) {
	store, request := forwardingRepositoryFixture(t)
	request.Name = "snapshot-vless-reality"
	request.ListenPort = 12000
	request.IngressProtocol = forwarding.IngressVLESSReality
	request.VLESSOutboundMode = forwarding.VLESSOutboundSOCKS5
	request.VLESSSOCKS5Host = "landing.example.test"
	request.VLESSSOCKS5Port = 1080
	request.VLESSSOCKS5Username = "landing-user"
	request.VLESSSOCKS5Password = "landing-secret"
	request.VLESSFlow = "xtls-rprx-vision"
	request.RealityServerName = "www.example.com"
	request.RealityPublicKey = "AbCdEf0123456789AbCdEf0123456789AbCdEf01234"
	request.RealityShortID = "0123456789abcdef"
	rule, _, err := forwarding.NewService(store, nil).Create(context.Background(), "admin", request, "snapshot-vless")
	if err != nil {
		t.Fatalf("create VLESS rule: %v", err)
	}

	raw, err := store.EncodeSnapshot()
	if err != nil {
		t.Fatalf("encode snapshot: %v", err)
	}
	restored, err := DecodeSnapshot(raw)
	if err != nil || restored == nil {
		t.Fatalf("VLESS snapshot round-trip failed: %v", err)
	}
	view, err := restored.ForwardingRule(context.Background(), rule.ID)
	if err != nil || view.ActivationReason != forwarding.ActivationReasonVLESSRuntimeMaterialPending {
		t.Fatalf("restored VLESS pending reason = %q, error=%v", view.ActivationReason, err)
	}

	var state snapshot
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	corruptRule := state.ForwardRules[rule.ID]
	corruptRule.ActivationReason = forwarding.ActivationReason("private-key-must-not-be-a-reason")
	state.ForwardRules[rule.ID] = corruptRule
	corrupt, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeSnapshot(corrupt); err == nil || !strings.Contains(err.Error(), "activation reason") {
		t.Fatalf("arbitrary activation reason accepted: %v", err)
	}

	corruptRule = state.ForwardRules[rule.ID]
	corruptRule.ActivationReason = forwarding.ActivationReasonVLESSRuntimeMaterialPending
	corruptRule.RealityPublicKey = "not-a-reality-key"
	state.ForwardRules[rule.ID] = corruptRule
	corrupt, err = json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeSnapshot(corrupt); err == nil || !strings.Contains(err.Error(), "forwarding configuration") {
		t.Fatalf("invalid Reality public key accepted: %v", err)
	}
}

func TestSnapshotKeyMemoStillValidatesEveryMaterial(t *testing.T) {
	for _, corrupt := range []string{"other-rule-key", "binding-customer", "missing-node", "revoked-with-secrets", "automatic-key"} {
		t.Run(corrupt, func(t *testing.T) {
			s, nodeIDs := compilerScaleIdentityFixture(t, 2, 3, true)
			baseline, err := s.EncodeSnapshotWithAudit(nil)
			if err != nil {
				t.Fatal(err)
			}
			s, err = DecodeSnapshot(baseline)
			if err != nil {
				t.Fatal("invalid baseline", err)
			}
			id := "material-compile-rule-0000-" + nodeIDs[1]
			material := s.vlessRuntimeMaterials[id]
			switch corrupt {
			case "other-rule-key":
				material.RealityPrivateKey = s.vlessRuntimeMaterials["material-compile-rule-0001-"+nodeIDs[1]].RealityPrivateKey
			case "binding-customer":
				material.CustomerID = "another-customer"
			case "missing-node":
				material.NodeID = "missing-node"
			case "revoked-with-secrets":
				material.State = vlessruntime.StateRevoked
				at := material.UpdatedAt
				material.RevokedAt = &at
			case "automatic-key":
				s.autoRealityKeys[material.ForwardingRuleID] = "invalid"
			}
			s.vlessRuntimeMaterials[id] = material
			for attempt := 0; attempt < 2; attempt++ {
				raw, err := s.EncodeSnapshotWithAudit(nil)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := DecodeSnapshot(raw); err == nil {
					t.Fatal("key memo accepted corrupted state")
				}
			}
		})
	}
}
