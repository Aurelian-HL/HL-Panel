package memoryrepo

import (
	"context"
	"encoding/base64"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/groupconfig"
	"github.com/hongle/hl-panel/internal/control/subscriptions"
	"github.com/hongle/hl-panel/internal/control/vlessconnection"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
	"gopkg.in/yaml.v3"
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

func TestNativeSubscriptionFollowsDeviceGroupDomainChange(t *testing.T) {
	f := newVLESSBundleFixture(t, true)
	ctx := context.Background()
	now := time.Now().UTC()

	// A bound endpoint must use the current entry-group address and the rule's
	// listener port. This mirrors the endpoint created by the production flow.
	rule := f.store.forwardRules[f.rule.ID]
	rule.Deployed = true
	rule.RealityDestination = "www.example.com:443"
	f.store.forwardRules[rule.ID] = rule
	pool := f.store.endpointPools[f.endpointID]
	pool.Hostname = f.store.groupNetworks[rule.EntryGroupID].ConnectHost
	pool.Port = rule.ListenPort
	f.store.endpointPools[f.endpointID] = pool
	member := f.store.endpointMembers[f.endpointID][f.nodeID]
	member.LastHealthAt = &now
	f.store.endpointMembers[f.endpointID][f.nodeID] = member

	identity, err := vlessidentity.NewService(f.store, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	service := subscriptions.NewService(f.store, vlessconnection.NewService(identity, forwarding.NewService(f.store, nil), endpoints.NewService(f.store, nil)), nil)
	request := subscriptions.Request{Name: rule.Name, CustomerID: rule.CustomerID, Lines: []subscriptions.Line{{Name: rule.Name, BindingID: f.bindingID}}}
	item, _, err := service.Generate(ctx, "admin", rule.ID, request, "group-domain-subscription")
	if err != nil {
		t.Fatal(err)
	}
	record, err := service.Detail(ctx, "admin", item.ID)
	if err != nil {
		t.Fatal(err)
	}
	oldRecord, oldLines, err := service.Public(ctx, record.Token)
	if err != nil || len(oldLines) != 1 || oldLines[0].URI == "" {
		t.Fatalf("initial subscription unavailable: err=%v", err)
	}
	oldURI, err := url.Parse(oldLines[0].URI)
	if err != nil || oldURI.Hostname() != "entry.example.test" {
		t.Fatalf("initial subscription endpoint = %q, err=%v", oldLines[0].URI, err)
	}

	network := f.store.groupNetworks[rule.EntryGroupID]
	updated, _, err := groupconfig.NewService(f.store, nil).Update(ctx, "admin", rule.EntryGroupID, groupconfig.Request{
		ConnectHost:         "new-entry.example.test",
		PortStart:           network.PortStart,
		PortEnd:             network.PortEnd,
		AllowDirect:         network.AllowDirect,
		AllowedExitGroupIDs: network.AllowedExitGroupIDs,
		TrafficMultiplier:   network.TrafficMultiplier,
		Revision:            network.Revision,
	}, "group-domain-change")
	if err != nil || updated.ConnectHost != "new-entry.example.test" {
		t.Fatalf("device group domain update failed: %+v err=%v", updated, err)
	}

	newRecord, newLines, err := service.Public(ctx, record.Token)
	if err != nil || len(newLines) != 1 || newLines[0].URI == "" {
		t.Fatalf("subscription after domain update unavailable: err=%v", err)
	}
	newURI, err := url.Parse(newLines[0].URI)
	if err != nil || newURI.Hostname() != "new-entry.example.test" {
		t.Fatalf("subscription kept old endpoint after domain update: %q err=%v", newLines[0].URI, err)
	}
	if newRecord.Token != oldRecord.Token || newURI.Port() != oldURI.Port() || newURI.User.String() != oldURI.User.String() || newURI.RawQuery != oldURI.RawQuery || newURI.Fragment != oldURI.Fragment {
		t.Fatal("domain update changed subscription token, listener port, credentials or protocol parameters")
	}
	if subscriptions.Paths(newRecord.Token) != subscriptions.Paths(oldRecord.Token) {
		t.Fatal("domain update changed public subscription paths")
	}
	txt, err := base64.StdEncoding.DecodeString(subscriptions.TXT(newLines))
	if err != nil || string(txt) != newLines[0].URI {
		t.Fatalf("TXT subscription did not contain the updated endpoint: err=%v", err)
	}
	body, err := subscriptions.YAML(newLines)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Proxies []struct {
			Server string `yaml:"server"`
			Port   int    `yaml:"port"`
			UUID   string `yaml:"uuid"`
		} `yaml:"proxies"`
	}
	if err := yaml.Unmarshal(body, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Proxies) != 1 || config.Proxies[0].Server != "new-entry.example.test" || config.Proxies[0].Port != rule.ListenPort || config.Proxies[0].UUID != oldURI.User.Username() {
		t.Fatal("Clash subscription did not preserve credentials at the updated endpoint")
	}
	snapshot, err := f.store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	restoredIdentity, err := vlessidentity.NewService(restored, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	restoredService := subscriptions.NewService(restored, vlessconnection.NewService(restoredIdentity, forwarding.NewService(restored, nil), endpoints.NewService(restored, nil)), nil)
	_, restoredLines, err := restoredService.Public(ctx, record.Token)
	if err != nil || len(restoredLines) != 1 || restoredLines[0].URI != newLines[0].URI {
		t.Fatalf("subscription domain change did not survive snapshot restore: err=%v", err)
	}
}
