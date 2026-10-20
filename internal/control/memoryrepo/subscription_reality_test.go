package memoryrepo

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/gatewaymembership"
	"github.com/hongle/hl-panel/internal/control/subscriptions"
	"github.com/hongle/hl-panel/internal/control/vlessconnection"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
)

func TestNativeSubscriptionFollowsRealityEditWithoutChangingIdentityOrURL(t *testing.T) {
	f := newVLESSBundleFixture(t, true)
	ctx := context.Background()
	now := time.Now().UTC()
	rule := f.store.forwardRules[f.rule.ID]
	rule.Deployed = true
	rule.RealityDestination = "www.example.com:443"
	f.store.forwardRules[rule.ID] = rule
	member := f.store.endpointMembers[f.endpointID][f.nodeID]
	member.LastHealthAt = &now
	f.store.endpointMembers[f.endpointID][f.nodeID] = member
	grantFixtureProtocolHealth(t, f.store, rule.ID, f.endpointID, f.nodeID)
	identity, err := vlessidentity.NewService(f.store, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	rules := forwarding.NewService(f.store, nil)
	service := subscriptions.NewService(f.store, vlessconnection.NewService(identity, rules, endpoints.NewServiceWithReadyCandidateSource(f.store, nil, gatewaymembership.NewService(f.store))), nil)
	item, _, err := service.Generate(ctx, "admin", rule.ID, subscriptions.Request{Name: rule.Name, CustomerID: rule.CustomerID, Lines: []subscriptions.Line{{Name: rule.Name, BindingID: f.bindingID}}}, "reality-feed")
	if err != nil {
		t.Fatal(err)
	}
	record, err := service.Detail(ctx, "admin", item.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, before, err := service.Public(ctx, record.Token)
	if err != nil || len(before) != 1 || before[0].URI == "" {
		t.Fatal("initial feed unavailable", err)
	}
	oldURI, err := url.Parse(before[0].URI)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(rule)
	if err != nil {
		t.Fatal(err)
	}
	var input forwarding.Request
	if err := json.Unmarshal(encoded, &input); err != nil {
		t.Fatal(err)
	}
	input.RealityServerName, input.RealityDestination = "www.apple.com", "www.apple.com:443"
	updated, _, err := rules.UpdateForAdministrator(ctx, "admin", rule.ID, input, "reality-edit")
	if err != nil {
		t.Fatal("Reality edit failed", err)
	}
	// Model the new configuration's node ACK and protocol verification. Until
	// then a pending rule must not be misrepresented as a deployed connection.
	updated.Deployed = true
	f.store.forwardRules[rule.ID] = updated
	grantFixtureProtocolHealth(t, f.store, rule.ID, f.endpointID, f.nodeID)
	afterRecord, after, err := service.Public(ctx, record.Token)
	if err != nil || len(after) != 1 || after[0].URI == "" {
		t.Fatal("updated feed unavailable", err)
	}
	uri, err := url.Parse(after[0].URI)
	if err != nil {
		t.Fatal(err)
	}
	if uri.Query().Get("sni") != "www.apple.com" {
		t.Fatal("feed kept old SNI")
	}
	oldQuery, query := oldURI.Query(), uri.Query()
	oldQuery.Del("sni")
	query.Del("sni")
	if oldQuery.Encode() != query.Encode() || oldURI.Host != uri.Host || oldURI.User.String() != uri.User.String() || oldURI.Fragment != uri.Fragment || afterRecord.Token != record.Token || subscriptions.Paths(afterRecord.Token) != subscriptions.Paths(record.Token) {
		t.Fatal("Reality edit changed unrelated connection or subscription parameters")
	}
	if f.store.vlessRuntimeMaterials[f.materialID].RealityPrivateKey != f.privateKey {
		t.Fatal("Reality edit rotated private key")
	}
	txt, err := base64.StdEncoding.DecodeString(subscriptions.TXT(after))
	if err != nil || string(txt) != after[0].URI {
		t.Fatal("TXT feed kept stale material", err)
	}
	yaml, err := subscriptions.YAML(after)
	if err != nil || !strings.Contains(string(yaml), "servername: www.apple.com") {
		t.Fatal("YAML feed kept stale SNI", err)
	}
}
