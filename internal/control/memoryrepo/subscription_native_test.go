package memoryrepo

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/subscriptions"
	"github.com/hongle/hl-panel/internal/control/vlessconnection"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
)

func TestSubscriptionNativeAccountFollowsRotationHealthAndRevocation(t *testing.T) {
	f := newVLESSBundleFixture(t, true)
	ctx := context.Background()
	now := time.Now().UTC()
	customer, _, err := customers.NewService(f.store, nil).CreateCustomer(ctx, "admin", customers.CustomerInput{Username: "native-subscriber", Password: "isolated", IdempotencyKey: "native-subscriber"})
	if err != nil {
		t.Fatal(err)
	}
	rule := f.store.forwardRules[f.rule.ID]
	rule.Deployed = true
	rule.RealityDestination = "www.example.com:443"
	f.store.forwardRules[rule.ID] = rule
	member := f.store.endpointMembers[f.endpointID][f.nodeID]
	member.LastHealthAt = &now
	f.store.endpointMembers[f.endpointID][f.nodeID] = member
	identity, err := vlessidentity.NewService(f.store, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	resolver := vlessconnection.NewService(identity, forwarding.NewService(f.store, nil), endpoints.NewService(f.store, nil))
	service := subscriptions.NewService(f.store, resolver, nil)
	request := subscriptions.Request{Name: "native", CustomerID: customer.ID, Lines: []subscriptions.Line{{Name: "stable line", BindingID: f.bindingID}}}
	item, _, err := service.Mutate(ctx, "admin", "", "create", request, "native-create")
	if err != nil {
		t.Fatal("administrator-owned credential could not be assigned", err)
	}
	item, _, err = service.Mutate(ctx, "admin", item.ID, "publish", subscriptions.Request{Revision: item.Revision}, "native-publish")
	if err != nil {
		t.Fatal(err)
	}
	record, err := service.Detail(ctx, "admin", item.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, lines, err := service.Public(ctx, record.Token)
	if err != nil || len(lines) != 1 || !strings.Contains(lines[0].URI, f.credential) || !strings.Contains(lines[0].URI, "vless.example.test:443") {
		t.Fatal("native stable endpoint not resolved", err, lines)
	}
	if _, err := resolver.ResolveSubscriptionLine(ctx, "other-admin", customer.ID, f.bindingID); err == nil {
		t.Fatal("another owner obtained credential")
	}
	// Current account material is resolved for every fetch, never cached in a feed.
	binding := f.store.vlessBindings[f.bindingID]
	binding.CredentialUUID = "4d506676-e698-44f0-b7ae-bc56ae48f394"
	f.store.vlessBindings[f.bindingID] = binding
	_, lines, err = service.Public(ctx, record.Token)
	if err != nil || strings.Contains(lines[0].URI, f.credential) || !strings.Contains(lines[0].URI, binding.CredentialUUID) {
		t.Fatal("native credential was cached")
	}
	member.LastHealthAt = nil
	f.store.endpointMembers[f.endpointID][f.nodeID] = member
	_, lines, err = service.Public(ctx, record.Token)
	if err != nil || lines[0].URI != "" || lines[0].Error == "" {
		t.Fatal("unhealthy native line was published")
	}
	member.LastHealthAt = &now
	f.store.endpointMembers[f.endpointID][f.nodeID] = member
	binding.Binding.State = vlessidentity.StateRevoked
	f.store.vlessBindings[f.bindingID] = binding
	_, lines, err = service.Public(ctx, record.Token)
	if err != nil || lines[0].URI != "" {
		t.Fatal("revoked native account was published")
	}
	if _, _, err := service.Mutate(ctx, "admin", "", "create", request, "revoked-create"); err == nil {
		t.Fatal("revoked account accepted in new draft")
	}
}
