package memoryrepo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/announcements"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/customeridentity"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/groupconfig"
)

func TestVersionFiveSnapshotUpgradesWithEmptyCustomerSessions(t *testing.T) {
	store, customer := customerRepositoryFixture(t)
	store.customers[customer.ID] = customer
	raw, err := store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	var legacy map[string]json.RawMessage
	if err := json.Unmarshal(raw, &legacy); err != nil {
		t.Fatal(err)
	}
	legacy["Version"] = json.RawMessage("5")
	delete(legacy, "CustomerSessions")
	raw, err = json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeSnapshot(raw)
	if err != nil {
		t.Fatalf("version 5 snapshot rejected: %v", err)
	}
	upgraded, err := restored.EncodeSnapshot()
	if err != nil || !bytes.Contains(upgraded, []byte(fmt.Sprintf(`"Version":%d`, SnapshotVersion))) || !bytes.Contains(upgraded, []byte(`"CustomerSessions":{}`)) {
		t.Fatalf("version 5 snapshot was not upgraded: %v", err)
	}
}

func newCustomerIdentityService(t *testing.T, store *Store, now time.Time) *customeridentity.Service {
	t.Helper()
	service, err := customeridentity.NewService(store.CustomerIdentityRepository("20261003"), func() time.Time { return now }, customeridentity.Options{
		SessionTTL: time.Hour, PasswordFingerprintKey: []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestCustomerIdentitySessionsPasswordChangeAndSnapshotAreIsolated(t *testing.T) {
	ctx := context.Background()
	store, customer := customerRepositoryFixture(t)
	store.customers[customer.ID] = customer
	now := time.Date(2026, time.October, 3, 1, 0, 0, 0, time.UTC)
	service := newCustomerIdentityService(t, store, now)

	first, err := service.Login(ctx, customer.Username, "customer-test-password")
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Login(ctx, customer.Username, "customer-test-password")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := service.Authenticate(ctx, first.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := service.ChangePassword(ctx, principal, "customer-test-password", "new-customer-password", "password-change-1")
	if err != nil || replayed {
		t.Fatalf("change password replayed=%v err=%v", replayed, err)
	}
	replayed, err = service.ChangePassword(ctx, principal, "customer-test-password", "new-customer-password", "password-change-1")
	if err != nil || !replayed {
		t.Fatalf("password replay replayed=%v err=%v", replayed, err)
	}
	if _, err := service.Authenticate(ctx, first.AccessToken); err != nil {
		t.Fatalf("current session was revoked: %v", err)
	}
	if _, err := service.Authenticate(ctx, second.AccessToken); !errors.Is(err, faults.ErrUnauthorized) {
		t.Fatalf("other customer session remains valid: %v", err)
	}
	if _, err := service.Login(ctx, customer.Username, "customer-test-password"); !errors.Is(err, faults.ErrUnauthorized) {
		t.Fatalf("old password remains valid: %v", err)
	}
	if _, err := service.Login(ctx, customer.Username, "new-customer-password"); err != nil {
		t.Fatalf("new password rejected: %v", err)
	}
	if len(store.sessionsByHash) != 0 {
		t.Fatal("customer authentication wrote administrator sessions")
	}

	raw, err := store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	restoredService := newCustomerIdentityService(t, restored, now)
	if _, err := restoredService.Authenticate(ctx, first.AccessToken); err != nil {
		t.Fatalf("snapshot lost current customer session: %v", err)
	}
	record, err := restored.CustomerIdentityRepository("20261003").CustomerByID(ctx, customer.ID)
	if err != nil || auth.VerifyPassword(record.Customer.PasswordHash, "new-customer-password") != nil || record.Customer.Revision != 2 {
		t.Fatalf("snapshot lost password mutation: revision=%d err=%v", record.Customer.Revision, err)
	}
}

func TestCustomerIdentityProjectsOnlyUsablePublicData(t *testing.T) {
	ctx := context.Background()
	store, customer := customerRepositoryFixture(t)
	customer.TrafficUsedBytes = 1024
	customer.TrafficLimitBytes = 4096
	store.customers[customer.ID] = customer
	other := customer
	other.ID, other.Username = "cus-other", "other"
	store.customers[other.ID] = other
	store.groupNetworks["entry-1"] = groupconfig.GroupNetwork{GroupID: "entry-1", ConnectHost: "entry.example.test", PortStart: 20000, PortEnd: 21000, DirectPolicy: groupconfig.DirectPolicyOptional, AllowDirect: true, TrafficMultiplier: 1, Revision: 1}
	store.forwardRules["rule-own"] = forwarding.Rule{
		ID: "rule-own", Name: "我的线路", CustomerID: customer.ID, EntryGroupID: "entry-1", EgressMode: forwarding.EgressDirect,
		Protocol: forwarding.ProtocolTCP, ListenPort: 20001, Targets: []forwarding.Target{{Host: "192.0.2.10", Port: 443}}, SelectionPolicy: forwarding.SelectionRoundRobin, Revision: 1,
	}
	store.forwardRules["rule-other"] = forwarding.Rule{ID: "rule-other", Name: "其他用户线路", CustomerID: other.ID, EntryGroupID: "entry-1", EgressMode: forwarding.EgressDirect, Protocol: forwarding.ProtocolTCP, ListenPort: 20002, Revision: 1}
	store.endpointPools["pool-vless"] = endpoints.EndpointPool{ID: "pool-vless", Name: "统一 VLESS 入口", GroupID: "entry-1", Mode: endpoints.ModeSingleServiceEndpoint, Protocol: "vless", Hostname: "vless.example.test", Port: 443}
	store.endpointMembers["pool-vless"] = map[string]endpoints.EndpointPoolMember{}
	store.siteSettings.SiteName = "测试站点"
	store.siteSettings.PanelTitle = "线路管理面板"
	store.siteSettings.PublicDescription = "公开说明"
	store.announcements["notice-1"] = announcements.Announcement{ID: "notice-1", Title: "站点公告", Content: "维护信息", Enabled: true, SortOrder: 1, CreatedAt: customer.CreatedAt, UpdatedAt: customer.UpdatedAt}
	now := time.Date(2026, time.October, 3, 1, 0, 0, 0, time.UTC)
	service := newCustomerIdentityService(t, store, now)
	login, err := service.Login(ctx, customer.Username, "customer-test-password")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := service.Authenticate(ctx, login.AccessToken)
	if err != nil {
		t.Fatal(err)
	}

	rules, err := service.Rules(ctx, principal)
	if err != nil || len(rules) != 1 || rules[0].ID != "rule-own" || rules[0].ConnectHost != "entry.example.test" || rules[0].RouteDescription != "entry · 直出" {
		t.Fatalf("unexpected customer rules: %+v err=%v", rules, err)
	}
	usage, err := service.Usage(ctx, principal)
	if err != nil || usage.TrafficUsedBytes != 1024 || usage.RemainingBytes == nil || *usage.RemainingBytes != 3072 {
		t.Fatalf("unexpected customer usage: %+v err=%v", usage, err)
	}
	subscriptions, err := service.Subscriptions(ctx, principal)
	if err != nil || len(subscriptions) != 1 || subscriptions[0].Protocol != "vless" || subscriptions[0].Endpoint != "vless.example.test:443" || subscriptions[0].URI != "" || subscriptions[0].Status != "identity_binding_required" {
		t.Fatalf("unexpected subscriptions: %+v err=%v", subscriptions, err)
	}
	portal, err := service.Portal(ctx)
	if err != nil || portal.SiteName != "测试站点" || portal.PanelVersion != "20261003" || portal.Announcement.Title != "站点公告" {
		t.Fatalf("unexpected portal projection: %+v err=%v", portal, err)
	}
}
