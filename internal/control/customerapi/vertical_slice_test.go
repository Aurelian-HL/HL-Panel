package customerapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/announcements"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/customerapi"
	"github.com/hongle/hl-panel/internal/control/customeridentity"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/groupconfig"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/control/rulegroups"
	"github.com/hongle/hl-panel/internal/control/siteconfig"
)

func verticalHandler(t *testing.T) http.Handler {
	handler, _, _, _ := verticalFixture(t)
	return handler
}

func verticalFixture(t *testing.T) (http.Handler, *memoryrepo.Store, string, string) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, time.October, 3, 2, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	adminHash, err := auth.HashPassword("administrator-password")
	if err != nil {
		t.Fatal(err)
	}
	store := memoryrepo.New(auth.Administrator{ID: "adm-test", Username: "admin", PasswordHash: adminHash, CreatedAt: now})
	group, err := groups.NewService(store, clock).Create(ctx, "adm-test", "广州入口", groups.KindEntry, endpoints.SelectionWeightedRoundRobin, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := groupconfig.NewService(store, clock).Update(ctx, "adm-test", group.ID, groupconfig.Request{
		ConnectHost: "entry.example.test", PortStart: 20000, PortEnd: 20100,
		DirectPolicy: groupconfig.DirectPolicyOptional, TrafficMultiplier: 1,
	}, "network-1"); err != nil {
		t.Fatal(err)
	}
	customerService := customers.NewService(store, clock)
	userGroup, _, err := customerService.CreateUserGroup(ctx, "adm-test", customers.UserGroupInput{
		Name: "普通用户", AllowedEntryGroupIDs: []string{group.ID}, AllowDirect: true, IdempotencyKey: "user-group-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	customer, _, err := customerService.CreateCustomer(ctx, "adm-test", customers.CustomerInput{
		Username: "member", DisplayName: "测试用户", UserGroupID: userGroup.ID, Password: "customer-password",
		TrafficLimitBytes: 4096, MaxRules: 10, SpeedLimitMbps: 200, IPLimit: 3, ConnectionLimit: 50,
		IdempotencyKey: "customer-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := forwarding.NewService(store, clock).Create(ctx, "adm-test", forwarding.Request{
		Name: "业务线路", CustomerID: customer.ID, EntryGroupID: group.ID, EgressMode: forwarding.EgressDirect,
		Protocol: forwarding.ProtocolTCP, Targets: []forwarding.Target{{Host: "192.0.2.10", Port: 443}},
		SelectionPolicy: forwarding.SelectionRoundRobin,
	}, "rule-1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := endpoints.NewService(store, clock).Create(ctx, "adm-test", "统一 VLESS 入口", group.ID,
		endpoints.ModeSingleServiceEndpoint, "vless", "vless.example.test", 443,
		endpoints.SelectionWeightedRoundRobin, "endpoint-1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := siteconfig.NewService(store, clock).Update(ctx, "adm-test", siteconfig.Request{
		SiteName: "测试站点", PanelTitle: "线路管理面板", PublicDescription: "公开服务说明", Theme: siteconfig.ThemeClassic,
	}, "site-1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := announcements.NewService(store, clock).Create(ctx, "adm-test", announcements.Request{
		Title: "站点公告", Content: "维护信息", Level: announcements.LevelInfo, Enabled: true,
	}, "notice-1"); err != nil {
		t.Fatal(err)
	}
	identity, err := customeridentity.NewService(store.CustomerIdentityRepository("20261003"), clock, customeridentity.Options{
		SessionTTL: time.Hour, PasswordFingerprintKey: []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return customerapi.New(identity, slog.New(slog.NewTextHandler(io.Discard, nil)), customerapi.WithRuleWrites(forwarding.NewService(store, clock), rulegroups.NewService(store, clock))), store, userGroup.ID, group.ID
}

func customerRequest(t *testing.T, handler http.Handler, method, path, token string, body any, headers map[string]string, status int) []byte {
	t.Helper()
	var raw []byte
	var err error
	if body != nil {
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(raw))
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != status {
		t.Fatalf("%s %s status=%d want=%d body=%s", method, path, response.Code, status, response.Body.String())
	}
	return response.Body.Bytes()
}

func customerLogin(t *testing.T, handler http.Handler, password string, status int) customeridentity.LoginResult {
	t.Helper()
	raw := customerRequest(t, handler, http.MethodPost, "/api/v1/customer/auth/login", "", map[string]string{
		"username": "member", "password": password,
	}, nil, status)
	if status != http.StatusOK {
		return customeridentity.LoginResult{}
	}
	var result customeridentity.LoginResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestCustomerVerticalSliceUsesPersistedBusinessState(t *testing.T) {
	handler := verticalHandler(t)
	login := customerLogin(t, handler, "customer-password", http.StatusOK)
	if login.AccessToken == "" || login.User.Username != "member" || login.User.UserGroupName != "普通用户" {
		t.Fatalf("unexpected login projection: %+v", login)
	}

	portal := string(customerRequest(t, handler, http.MethodGet, "/api/v1/customer/portal", "", nil, nil, http.StatusOK))
	if !strings.Contains(portal, "测试站点") || !strings.Contains(portal, "站点公告") || !strings.Contains(portal, "20261003") {
		t.Fatalf("portal omitted configured public state: %s", portal)
	}
	profile := string(customerRequest(t, handler, http.MethodGet, "/api/v1/customer/me?customer_id=another", login.AccessToken, nil, nil, http.StatusOK))
	if !strings.Contains(profile, `"username":"member"`) || !strings.Contains(profile, `"speed_limit_mbps":200`) || strings.Contains(profile, "another") || strings.Contains(strings.ToLower(profile), "password") {
		t.Fatalf("unsafe or incomplete customer profile: %s", profile)
	}
	rules := string(customerRequest(t, handler, http.MethodGet, "/api/v1/customer/rules", login.AccessToken, nil, nil, http.StatusOK))
	if !strings.Contains(rules, "业务线路") || !strings.Contains(rules, "entry.example.test") || !strings.Contains(rules, "直出") {
		t.Fatalf("rule projection missing: %s", rules)
	}
	connections := string(customerRequest(t, handler, http.MethodGet, "/api/v1/customer/connections", login.AccessToken, nil, nil, http.StatusOK))
	if !strings.Contains(connections, `"status":"unavailable"`) || strings.Contains(connections, "vless://") || strings.Contains(connections, "identity_binding_required") {
		t.Fatalf("unverified connection projection is unsafe: %s", connections)
	}
	customerRequest(t, handler, http.MethodGet, "/api/v1/customer/subscriptions", login.AccessToken, nil, nil, http.StatusNotFound)
	usage := string(customerRequest(t, handler, http.MethodGet, "/api/v1/customer/usage", login.AccessToken, nil, nil, http.StatusOK))
	if !strings.Contains(usage, `"traffic_limit_bytes":4096`) {
		t.Fatalf("usage projection missing: %s", usage)
	}

	customerRequest(t, handler, http.MethodPut, "/api/v1/customer/password", login.AccessToken, map[string]string{
		"current_password": "customer-password", "new_password": "new-customer-password",
	}, map[string]string{"Idempotency-Key": "change-password-1"}, http.StatusOK)
	customerLogin(t, handler, "customer-password", http.StatusUnauthorized)
	customerLogin(t, handler, "new-customer-password", http.StatusOK)
}

func TestCustomerRuleMutationsUseAuthenticatedOwner(t *testing.T) {
	handler, store, userGroupID, entryID := verticalFixture(t)
	ctx := context.Background()
	other, _, err := customers.NewService(store, time.Now).CreateCustomer(ctx, "adm-test", customers.CustomerInput{
		Username: "other", DisplayName: "另一个用户", UserGroupID: userGroupID,
		Password: "other-password", MaxRules: 10, IdempotencyKey: "other-customer",
	})
	if err != nil {
		t.Fatal(err)
	}
	owner := customerLogin(t, handler, "customer-password", http.StatusOK)
	var otherLogin customeridentity.LoginResult
	otherRaw := customerRequest(t, handler, http.MethodPost, "/api/v1/customer/auth/login", "", map[string]string{
		"username": "other", "password": "other-password",
	}, nil, http.StatusOK)
	if err := json.Unmarshal(otherRaw, &otherLogin); err != nil {
		t.Fatal(err)
	}
	options := string(customerRequest(t, handler, http.MethodGet, "/api/v1/customer/rule-options", owner.AccessToken, nil, nil, http.StatusOK))
	if !strings.Contains(options, entryID) || !strings.Contains(options, `"allow_direct":true`) {
		t.Fatalf("authorized entry group missing from rule options: %s", options)
	}
	input := forwarding.Request{
		Name: "我的新线路", CustomerID: other.ID, EntryGroupID: entryID,
		EgressMode: forwarding.EgressDirect, Protocol: forwarding.ProtocolTCP,
		Targets:         []forwarding.Target{{Host: "192.0.2.20", Port: 443}},
		SelectionPolicy: forwarding.SelectionRoundRobin,
	}
	var created struct {
		Rule forwarding.Rule `json:"rule"`
	}
	createdRaw := customerRequest(t, handler, http.MethodPost, "/api/v1/customer/rules", owner.AccessToken, input,
		map[string]string{"Idempotency-Key": "owner-create"}, http.StatusCreated)
	if err := json.Unmarshal(createdRaw, &created); err != nil {
		t.Fatal(err)
	}
	if created.Rule.CustomerID != owner.User.ID || created.Rule.Revision != 1 {
		t.Fatalf("rule owner was taken from request body: %+v", created.Rule)
	}
	path := "/api/v1/customer/rules/" + created.Rule.ID
	customerRequest(t, handler, http.MethodGet, path, otherLogin.AccessToken, nil, nil, http.StatusNotFound)
	input.Revision = created.Rule.Revision
	customerRequest(t, handler, http.MethodPut, path, otherLogin.AccessToken, input,
		map[string]string{"Idempotency-Key": "foreign-update"}, http.StatusNotFound)
	foreignDelete := rulegroups.BatchRequest{Operation: rulegroups.BatchDelete, RuleIDs: []string{created.Rule.ID},
		ExpectedRevisions: map[string]int64{created.Rule.ID: created.Rule.Revision}}
	customerRequest(t, handler, http.MethodPost, "/api/v1/customer/rules/batch", otherLogin.AccessToken, foreignDelete,
		map[string]string{"Idempotency-Key": "foreign-delete"}, http.StatusNotFound)
	otherRules := string(customerRequest(t, handler, http.MethodGet, "/api/v1/customer/rules", otherLogin.AccessToken, nil, nil, http.StatusOK))
	if strings.Contains(otherRules, created.Rule.ID) {
		t.Fatalf("foreign rule appeared in customer list: %s", otherRules)
	}
	input.Name = "修改后的线路"
	var updated struct {
		Rule forwarding.Rule `json:"rule"`
	}
	updatedRaw := customerRequest(t, handler, http.MethodPut, path, owner.AccessToken, input,
		map[string]string{"Idempotency-Key": "owner-update"}, http.StatusOK)
	if err := json.Unmarshal(updatedRaw, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Rule.CustomerID != owner.User.ID || updated.Rule.Revision != 2 {
		t.Fatalf("unexpected owner or revision after update: %+v", updated.Rule)
	}
	pause := rulegroups.BatchRequest{Operation: rulegroups.BatchPause, RuleIDs: []string{created.Rule.ID},
		ExpectedRevisions: map[string]int64{created.Rule.ID: 2}}
	customerRequest(t, handler, http.MethodPost, "/api/v1/customer/rules/batch", owner.AccessToken, pause,
		map[string]string{"Idempotency-Key": "owner-pause"}, http.StatusOK)
	foreignDelete.ExpectedRevisions[created.Rule.ID] = 3
	customerRequest(t, handler, http.MethodPost, "/api/v1/customer/rules/batch", owner.AccessToken, foreignDelete,
		map[string]string{"Idempotency-Key": "owner-delete"}, http.StatusOK)
	customerRequest(t, handler, http.MethodGet, path, owner.AccessToken, nil, nil, http.StatusNotFound)
}
