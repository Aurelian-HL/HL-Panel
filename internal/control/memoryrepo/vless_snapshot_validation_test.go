package memoryrepo

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hongle/hl-panel/internal/control/forwarding"
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
