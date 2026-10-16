package httpapi_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/groupconfig"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
	"github.com/hongle/hl-panel/internal/control/vlesssetup"
)

func customerVLESSRequest(f businessFixture) forwarding.Request {
	r := f.ruleRequest()
	r.Targets = nil
	r.IngressProtocol = forwarding.IngressVLESSReality
	r.Protocol = forwarding.ProtocolTCP
	r.EgressMode = forwarding.EgressDirect
	r.VLESSOutboundMode = forwarding.VLESSOutboundSOCKS5
	r.VLESSSOCKS5Host = "landing.example.test"
	r.VLESSSOCKS5Port = 1080
	r.VLESSSOCKS5Username = "test-user"
	r.VLESSSOCKS5Password = "isolated-secret"
	r.RealityServerName = "example.com"
	r.RealityDestination = "example.com:443"
	return r
}

func TestCustomerVLESSCreationProvisionAndRestartRecovery(t *testing.T) {
	f := newBusinessFixture(t)
	ctx := context.Background()
	input := customerVLESSRequest(f)
	path := "/api/v1/customers/" + f.customer.ID + "/forwarding-rules"
	var rule forwarding.Rule
	decodeBusinessEntity(t, businessRequest(t, f, http.MethodPost, path, "customer-vless-auto", input, http.StatusCreated), "rule", &rule)
	identities, err := vlessidentity.NewService(f.store, time.Now, nil)
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := identities.List(ctx, "adm_business")
	deadline := time.Now().Add(5 * time.Second)
	for err == nil && len(bindings) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		bindings, err = identities.List(ctx, "adm_business")
	}
	if err != nil || len(bindings) != 1 || bindings[0].ForwardingRuleID != rule.ID || bindings[0].CustomerID != f.customer.ID {
		t.Fatalf("customer rule did not automatically receive an identity: count=%d err=%v", len(bindings), err)
	}
	original, err := identities.CredentialForAdministrator(ctx, "adm_business", rule.ID)
	if err != nil || original.CredentialUUID == "" {
		t.Fatal("authorized connection credential missing", err)
	}
	businessRequest(t, f, http.MethodPost, path, "customer-vless-auto", input, http.StatusOK)
	bindings, _ = identities.List(ctx, "adm_business")
	if len(bindings) != 1 {
		t.Fatal("replay duplicated customer identity")
	}
	if _, err = identities.CredentialForAdministrator(ctx, "unrelated-admin", rule.ID); !errors.Is(err, faults.ErrNotFound) {
		t.Fatal("unrelated administrator received customer credential", err)
	}
	// Simulate a request interrupted between the durable rule and setup writes.
	forwardingService := forwarding.NewService(f.store, time.Now)
	pending, _, err := forwardingService.CreateForCustomerByAdministrator(ctx, "adm_business", f.customer.ID, input, "interrupted-customer-vless")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := memoryrepo.DecodeSnapshot(snapshot)
	if err != nil {
		t.Fatal("customer-owned identity snapshot restore failed", err)
	}
	forwardingService = forwarding.NewService(restored, time.Now)
	identities, err = vlessidentity.NewService(restored, time.Now, nil)
	if err != nil {
		t.Fatal(err)
	}
	setup := vlesssetup.NewService(groupconfig.NewService(restored, time.Now), endpoints.NewService(restored, time.Now), identities, vlesssetup.WithRuleAccess(forwardingService))
	if err = setup.Reconcile(ctx, forwardingService); err != nil {
		t.Fatal("restart recovery", err)
	}
	if err = setup.Reconcile(ctx, forwardingService); err != nil {
		t.Fatal("repeated recovery", err)
	}
	bindings, _ = identities.List(ctx, "adm_business")
	if len(bindings) != 2 {
		t.Fatalf("recovery must create exactly one missing identity, got %d", len(bindings))
	}
	recovered, err := identities.CredentialForAdministrator(ctx, "adm_business", rule.ID)
	if err != nil || recovered.CredentialUUID != original.CredentialUUID {
		t.Fatal("restart rotated existing customer identity", err)
	}
	if _, err = identities.CredentialForAdministrator(ctx, "adm_business", pending.ID); err != nil {
		t.Fatal("pending rule was not recovered", err)
	}
	if err = setup.Ensure(ctx, "unrelated-admin", pending); !errors.Is(err, faults.ErrNotFound) {
		t.Fatal("setup crossed administrator boundary", err)
	}
}
