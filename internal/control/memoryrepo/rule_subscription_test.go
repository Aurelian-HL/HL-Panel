package memoryrepo

import (
	"context"
	"fmt"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/subscriptions"
	"github.com/hongle/hl-panel/internal/control/vlessconnection"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAutomaticRuleSubscriptionConcurrentClicksAndQuota(t *testing.T) {
	f := newVLESSBundleFixture(t, true)
	ctx := context.Background()
	identity, _ := vlessidentity.NewService(f.store, nil, nil)
	service := subscriptions.NewService(f.store, vlessconnection.NewService(identity, forwarding.NewService(f.store, nil), endpoints.NewService(f.store, nil)), nil)
	rule := f.store.forwardRules[f.rule.ID]
	rule.Deployed = true
	rule.RealityDestination = "www.example.com:443"
	rule.TrafficLimitBytes = 100
	f.store.forwardRules[rule.ID] = rule
	member := f.store.endpointMembers[f.endpointID][f.nodeID]
	now := time.Now().UTC()
	member.LastHealthAt = &now
	f.store.endpointMembers[f.endpointID][f.nodeID] = member
	request := subscriptions.Request{Name: rule.Name, CustomerID: rule.CustomerID, Lines: []subscriptions.Line{{Name: rule.Name, BindingID: f.bindingID}}}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, err := service.Generate(ctx, "admin", rule.ID, request, fmt.Sprintf("generate-%d", i))
			if err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	items, err := service.List(ctx, "admin")
	if err != nil || len(items) != 1 || items[0].PendingUpdate || items[0].PublishedRevision != 1 {
		t.Fatal("not one atomic published subscription", items, err)
	}
	item := items[0]
	record, _ := service.Detail(ctx, "admin", item.ID)
	_, lines, err := service.Public(ctx, record.Token)
	if err != nil || len(lines) != 1 || lines[0].URI == "" {
		t.Fatal("administrator auto feed unavailable", err, lines)
	}
	// Updating the native account changes the same feed, without caching its URI.
	rule.Name = "随遇而安"
	f.store.forwardRules[rule.ID] = rule
	afterRename, _ := service.Detail(ctx, "admin", item.ID)
	_, renamedLines, renameErr := service.Public(ctx, record.Token)
	parsed, _ := url.Parse(renamedLines[0].URI)
	listed, _ := service.List(ctx, "admin")
	if renameErr != nil || afterRename.Token != record.Token || afterRename.Item.Name != rule.Name || listed[0].Name != rule.Name || renamedLines[0].Name != rule.Name || parsed.Fragment != rule.Name || afterRename.Item.Revision != record.Item.Revision {
		t.Fatal("rename failed to update current rule nickname with fixed address")
	}
	binding := f.store.vlessBindings[f.bindingID]
	binding.CredentialUUID = "4d506676-e698-44f0-b7ae-bc56ae48f394"
	f.store.vlessBindings[f.bindingID] = binding
	_, lines, err = service.Public(ctx, record.Token)
	if err != nil || lines[0].URI == "" {
		t.Fatal(err)
	}
	if _, _, err = service.Generate(ctx, "other-admin", rule.ID, request, "other"); err == nil {
		t.Fatal("ownership bypass")
	}
	if _, _, err = service.Mutate(ctx, "admin", item.ID, "update", subscriptions.Request{Name: "hijack", CustomerID: rule.CustomerID, Revision: item.Revision, Lines: request.Lines}, "hijack"); err == nil {
		t.Fatal("automatic source changed manually")
	}
	if err = f.store.SyncRuleTraffic(ctx, rule.ID, 100, now); err != nil {
		t.Fatal(err)
	}
	_, lines, err = service.Public(ctx, record.Token)
	if err != nil || len(lines) != 1 || lines[0].URI != "" {
		t.Fatal("exhausted rule still supplied", lines, err)
	}
	if !strings.Contains(lines[0].Error, "额度已用完") {
		t.Fatal("missing quota reason", lines[0].Error)
	}
	if _, err := subscriptions.Package(afterRename, lines, "https://panel.example.test"); err == nil {
		t.Fatal("quota allowed empty ZIP")
	}
	after, _ := service.Detail(ctx, "admin", item.ID)
	if after.Token != record.Token {
		t.Fatal("quota rotated feed")
	}
	request.Name = "renamed"
	request.Lines[0].Name = "renamed"
	renamed, _, err := service.Generate(ctx, "admin", rule.ID, request, "rename")
	if err != nil || renamed.ID != item.ID || renamed.Revision != 2 {
		t.Fatal("rename duplicated auto feed", err)
	}
	after, _ = service.Detail(ctx, "admin", item.ID)
	if after.Token != record.Token {
		t.Fatal("rename rotated feed")
	}
	_, _, err = service.Mutate(ctx, "admin", item.ID, "revoke", subscriptions.Request{Revision: renamed.Revision}, "revoke")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = service.Generate(ctx, "admin", rule.ID, request, "must-not-restore"); err == nil {
		t.Fatal("revoked feed silently restored")
	}
}
