package postgressnapshot

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/groupconfig"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
	"github.com/hongle/hl-panel/internal/control/vlesssetup"
)

func TestPostgreSQLCustomerVLESSRestartRecovery(t *testing.T) {
	dsn := isolatedDatabaseURL(t)
	ctx := context.Background()
	admin := bootstrapAdministrator(t)
	s, err := Open(ctx, dsn, admin)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	now := time.Now().UTC()
	event := audit.Event{ID: "business-event", Action: "business-test", ActorID: admin.ID, CreatedAt: now}
	require := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	require(s.CreateDeviceGroup(ctx, groups.DeviceGroup{ID: "entry-business", Name: "Business entry", Kind: groups.KindEntry}, event))
	networkInput := groupconfig.UpdateInput{Network: groupconfig.GroupNetwork{GroupID: "entry-business", ConnectHost: "entry.example.test", PortStart: 22000, PortEnd: 22010, AllowDirect: true, TrafficMultiplier: 1, Revision: 1, UpdatedAt: now}, IdempotencyKey: "network-configure", RequestSHA256: "network-request", CreatedBy: admin.ID}
	_, _, err = s.UpdateGroupNetwork(ctx, networkInput, event)
	require(err)
	group := customers.UserGroup{ID: "business-user-group", Name: "Business users", AllowedEntryGroupIDs: []string{"entry-business"}, AllowedExitGroupIDs: []string{}, AllowDirect: true, Revision: 1, CreatedAt: now, UpdatedAt: now}
	groupInput := customers.SaveUserGroupInput{UserGroup: group, IdempotencyKey: "create-group", RequestSHA256: "group-request", CreatedBy: admin.ID}
	_, _, err = s.SaveUserGroup(ctx, groupInput, event)
	require(err)
	customerService := customers.NewService(s, nil)
	customerInput := customers.CustomerInput{Username: "business-customer", Password: "isolated-customer-password", UserGroupID: group.ID, MaxRules: 2, IdempotencyKey: "create-customer"}
	customer, _, err := customerService.CreateCustomer(ctx, admin.ID, customerInput)
	require(err)

	fs := forwarding.NewService(s, time.Now, forwarding.WithRealityDefaults(forwarding.RealityDefaults{ServerName: "example.com", Destination: "example.com:443"}))
	request := forwarding.Request{Name: "Customer VLESS", EntryGroupID: "entry-business", EgressMode: forwarding.EgressDirect, IngressProtocol: forwarding.IngressVLESSReality, Protocol: forwarding.ProtocolTCP, VLESSOutboundMode: forwarding.VLESSOutboundSOCKS5, VLESSSOCKS5Host: "landing.example.test", VLESSSOCKS5Port: 1080, VLESSSOCKS5Username: "test-user", VLESSSOCKS5Password: "isolated-secret", SelectionPolicy: forwarding.SelectionRoundRobin}
	rule, _, err := fs.CreateForCustomerByAdministrator(ctx, admin.ID, customer.ID, request, "vless-rule")
	require(err)
	// Restart after rule commit, before setup; resume with the durable actor.
	require(s.Close())
	s, err = Open(ctx, dsn, auth.Administrator{})
	require(err)
	fs = forwarding.NewService(s, time.Now)
	actor, err := fs.VLESSSetupAdministrator(ctx, rule.ID)
	require(err)
	if actor != admin.ID {
		t.Fatal("creation actor was not recovered")
	}
	identities, err := vlessidentity.NewService(s, time.Now, nil)
	require(err)
	setup := vlesssetup.NewService(groupconfig.NewService(s, time.Now), endpoints.NewService(s, time.Now), identities, vlesssetup.WithRuleAccess(fs))
	require(setup.Reconcile(ctx, fs))
	require(setup.Reconcile(ctx, fs))
	credential, err := identities.CredentialForAdministrator(ctx, admin.ID, rule.ID)
	require(err)
	if credential.CredentialUUID == "" {
		t.Fatal("missing credential")
	}
	if _, err = identities.CredentialForAdministrator(ctx, "unrelated-admin", rule.ID); !errors.Is(err, faults.ErrNotFound) {
		t.Fatal("credential authorization failed")
	}
	if _, replay, err := fs.CreateForCustomerByAdministrator(ctx, admin.ID, customer.ID, request, "vless-rule"); err != nil || !replay {
		t.Fatal("rule replay failed", err)
	}
	require(s.Close())
	reopened, err := Open(ctx, dsn, auth.Administrator{})
	require(err)
	defer reopened.Close()
	identities, err = vlessidentity.NewService(reopened, time.Now, nil)
	require(err)
	after, err := identities.CredentialForAdministrator(ctx, admin.ID, rule.ID)
	require(err)
	if after.CredentialUUID != credential.CredentialUUID {
		t.Fatal("restart rotated credential")
	}
	bindings, err := identities.List(ctx, admin.ID)
	require(err)
	if len(bindings) != 1 {
		t.Fatal("setup duplicated identity")
	}
	restored, err := reopened.ForwardingRule(ctx, rule.ID)
	require(err)
	if restored.ID != rule.ID || restored.ListenPort != rule.ListenPort || restored.RealityPublicKey != rule.RealityPublicKey {
		t.Fatal("restart changed connection parameters")
	}
}
