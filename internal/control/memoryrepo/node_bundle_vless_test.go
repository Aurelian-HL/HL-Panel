package memoryrepo

import (
	"context"
	"errors"
	"testing"

	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/nodes"
)

func TestVLESSRealityRuleStaysPendingAndIsExcludedFromNodeBundle(t *testing.T) {
	store, request := forwardingRepositoryFixture(t)
	store.nodes["vless-node"] = nodes.Node{ID: "vless-node", Name: "isolated-vless-node"}
	if _, _, err := groups.NewService(store, nil).AddMember(context.Background(), "admin", request.EntryGroupID, "vless-node", 1, 0); err != nil {
		t.Fatalf("add node to entry group: %v", err)
	}

	request.Name = "vless-reality-pending"
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

	rule, replayed, err := forwarding.NewService(store, nil).Create(context.Background(), "admin", request, "vless-pending-bundle")
	if err != nil {
		t.Fatalf("create VLESS rule: %v", err)
	}
	if replayed {
		t.Fatal("first VLESS rule creation unexpectedly replayed")
	}
	if rule.Status != forwarding.StatusPendingActivation || rule.Deployed {
		t.Fatalf("VLESS rule lifecycle = status %q deployed %v, want pending and false", rule.Status, rule.Deployed)
	}
	if got := rule.ActivationReason; got != forwarding.ActivationReasonVLESSRuntimeMaterialPending {
		t.Fatalf("activation reason = %q, want %q", got, forwarding.ActivationReasonVLESSRuntimeMaterialPending)
	}
	events := store.AuditEvents()
	if len(events) == 0 || events[len(events)-1].Metadata["activation_reason"] != string(forwarding.ActivationReasonVLESSRuntimeMaterialPending) {
		t.Fatalf("VLESS pending reason was not recorded in audit metadata: %#v", events)
	}

	// A complete public Reality form is still not enough to build a node
	// listener: UUID and node-local private material are intentionally absent.
	fragments, err := store.compileForwardingFragmentsLocked("vless-node")
	if err != nil {
		t.Fatalf("compile forwarding fragments: %v", err)
	}
	if len(fragments) != 0 {
		t.Fatalf("VLESS rule produced executable fragments: %#v", fragments)
	}
	if got := store.nodes["vless-node"].DesiredGeneration; got != 0 {
		t.Fatalf("VLESS-only node advanced desired generation to %d", got)
	}
	if _, _, err := store.DesiredNodeConfig(context.Background(), "vless-node"); !errors.Is(err, faults.ErrNotFound) {
		t.Fatalf("VLESS-only node unexpectedly has desired config: %v", err)
	}

	view, err := forwarding.NewService(store, nil).Get(context.Background(), rule.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.ActivationReason != forwarding.ActivationReasonVLESSRuntimeMaterialPending || view.Deployed {
		t.Fatalf("stored VLESS view lost pending projection: %+v", view)
	}
}
