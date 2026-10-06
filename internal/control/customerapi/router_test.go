package customerapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/customeridentity"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/faults"
)

type apiRepository struct {
	record       customeridentity.CustomerRecord
	sessions     map[string]customeridentity.Session
	passwordKeys map[string]string
}

func newAPIRepository(t *testing.T, now time.Time) *apiRepository {
	t.Helper()
	hash, err := auth.HashPassword("password")
	if err != nil {
		t.Fatal(err)
	}
	return &apiRepository{
		record: customeridentity.CustomerRecord{Customer: customers.Customer{
			ID: "cus_api", Username: "member", DisplayName: "测试用户", UserGroupID: "ug_api", PasswordHash: hash,
			TrafficUsedBytes: 1024, TrafficLimitBytes: 4096, MaxRules: 3, SpeedLimitMbps: 100,
			CreatedAt: now, UpdatedAt: now, Revision: 1,
		}, UserGroupName: "普通用户"},
		sessions: map[string]customeridentity.Session{}, passwordKeys: map[string]string{},
	}
}

func (r *apiRepository) CustomerByUsername(_ context.Context, username string) (customeridentity.CustomerRecord, error) {
	if username != r.record.Customer.Username {
		return customeridentity.CustomerRecord{}, faults.ErrNotFound
	}
	return r.record, nil
}

func (r *apiRepository) CustomerByID(_ context.Context, id string) (customeridentity.CustomerRecord, error) {
	if id != r.record.Customer.ID {
		return customeridentity.CustomerRecord{}, faults.ErrNotFound
	}
	return r.record, nil
}

func (r *apiRepository) CreateSession(_ context.Context, session customeridentity.Session, _ audit.Event) error {
	r.sessions[session.TokenHash] = session
	return nil
}

func (r *apiRepository) SessionByTokenHash(_ context.Context, hash string, now time.Time) (customeridentity.Session, error) {
	session, ok := r.sessions[hash]
	if !ok || !now.Before(session.ExpiresAt) {
		return customeridentity.Session{}, faults.ErrUnauthorized
	}
	return session, nil
}

func (r *apiRepository) RevokeSession(_ context.Context, hash, customerID string, _ audit.Event) error {
	session, ok := r.sessions[hash]
	if !ok || session.CustomerID != customerID {
		return faults.ErrUnauthorized
	}
	delete(r.sessions, hash)
	return nil
}

func (r *apiRepository) PasswordChangeReplay(_ context.Context, customerID, key, fingerprint string) (bool, error) {
	value, ok := r.passwordKeys[customerID+"\x00"+key]
	if !ok {
		return false, nil
	}
	if value != fingerprint {
		return false, faults.ErrIdempotencyConflict
	}
	return true, nil
}

func (r *apiRepository) ChangePassword(_ context.Context, input customeridentity.ChangePasswordInput, _ audit.Event) (bool, error) {
	if input.ExpectedRevision != r.record.Customer.Revision {
		return false, faults.ErrConflict
	}
	r.record.Customer.PasswordHash = append([]byte(nil), input.PasswordHash...)
	r.record.Customer.Revision++
	r.passwordKeys[input.CustomerID+"\x00"+input.IdempotencyKey] = input.RequestHMACSHA256
	return false, nil
}

func (r *apiRepository) ListRulesByCustomer(_ context.Context, customerID string) ([]customeridentity.RuleRecord, error) {
	return []customeridentity.RuleRecord{{
		CustomerID: customerID, ID: "rule_api", Name: "我的线路", Protocol: "tcp", ConnectHost: "entry.example.com",
		ListenPort: 12001, RouteDescription: "入口直出", Targets: []customeridentity.TargetView{{Host: "target.example.com", Port: 443}}, Status: "pending_activation",
	}}, nil
}

func (r *apiRepository) RuleOptionsByCustomer(context.Context, string) (customeridentity.RuleOptionsView, error) {
	return customeridentity.RuleOptionsView{}, nil
}

func (r *apiRepository) CustomerUsage(_ context.Context, customerID string) (customeridentity.UsageRecord, error) {
	return customeridentity.UsageRecord{CustomerID: customerID, TrafficUsedBytes: 1024, TrafficLimitBytes: 4096, UpdatedAt: r.record.Customer.UpdatedAt}, nil
}

func (r *apiRepository) ListSubscriptionsByCustomer(_ context.Context, customerID string) ([]customeridentity.SubscriptionRecord, error) {
	return []customeridentity.SubscriptionRecord{{CustomerID: customerID, ID: "sub_api", RuleID: "rule_api", Name: "单链接", Protocol: "vless", Endpoint: "entry.example.com:443", Status: "active", Ready: true, URI: "vless://uuid@entry.example.com:443?security=tls"}}, nil
}

func (r *apiRepository) Portal(context.Context) (customeridentity.PortalView, error) {
	return customeridentity.PortalView{SiteName: "XZPanel", PanelVersion: "0.1.0", Announcement: customeridentity.AnnouncementView{Title: "欢迎使用", Content: "服务公告"}}, nil
}

func (r *apiRepository) AppendAudit(context.Context, audit.Event) error { return nil }

func newAPIHandler(t *testing.T, repository customeridentity.Repository, now time.Time) http.Handler {
	t.Helper()
	service, err := customeridentity.NewService(repository, func() time.Time { return now }, customeridentity.Options{
		SessionTTL: time.Hour, PasswordFingerprintKey: []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return New(service, nil)
}

func request(t *testing.T, handler http.Handler, method, path, token string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	var err error
	if body != nil {
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	return response
}

func loginAPI(t *testing.T, handler http.Handler) string {
	t.Helper()
	response := request(t, handler, http.MethodPost, "/api/v1/customer/auth/login", "", map[string]string{"username": "member", "password": "password"}, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("login status = %d, body=%s", response.Code, response.Body.String())
	}
	var result customeridentity.LoginResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result.AccessToken
}

func TestCustomerAPIPortalAndAuthenticatedOwnViews(t *testing.T) {
	now := time.Date(2026, 10, 3, 2, 0, 0, 0, time.UTC)
	handler := newAPIHandler(t, newAPIRepository(t, now), now)
	portal := request(t, handler, http.MethodGet, "/api/v1/customer/portal", "", nil, nil)
	if portal.Code != http.StatusOK || portal.Header().Get("Cache-Control") != "no-store" || !strings.Contains(portal.Body.String(), "XZPanel") {
		t.Fatalf("portal response = %d %#v %s", portal.Code, portal.Header(), portal.Body.String())
	}
	token := loginAPI(t, handler)
	for _, path := range []string{
		"/api/v1/customer/me?customer_id=cus_other",
		"/api/v1/customer/rules?customer_id=cus_other",
		"/api/v1/customer/usage?customer_id=cus_other",
		"/api/v1/customer/connections?customer_id=cus_other",
	} {
		response := request(t, handler, http.MethodGet, path, token, nil, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, body=%s", path, response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), "cus_other") {
			t.Fatalf("GET %s trusted caller customer id: %s", path, response.Body.String())
		}
	}
	legacy := request(t, handler, http.MethodGet, "/api/v1/customer/subscriptions", token, nil, nil)
	if legacy.Code != http.StatusNotFound {
		t.Fatalf("legacy subscription route status = %d, want 404", legacy.Code)
	}
}

func TestCustomerAPIRejectsMissingTokenAndNeverLeaksPasswordHash(t *testing.T) {
	now := time.Date(2026, 10, 3, 2, 0, 0, 0, time.UTC)
	handler := newAPIHandler(t, newAPIRepository(t, now), now)
	unauthorized := request(t, handler, http.MethodGet, "/api/v1/customer/me", "", nil, nil)
	if unauthorized.Code != http.StatusUnauthorized || unauthorized.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Fatalf("unauthorized response = %d %#v", unauthorized.Code, unauthorized.Header())
	}
	login := request(t, handler, http.MethodPost, "/api/v1/customer/auth/login", "", map[string]string{"username": "member", "password": "password"}, nil)
	body := strings.ToLower(login.Body.String())
	if strings.Contains(body, "password") || strings.Contains(body, "hash") || strings.Contains(body, "pbkdf2") {
		t.Fatalf("login response leaked password material: %s", login.Body.String())
	}
}

func TestCustomerAPIPasswordChangeRequiresIdempotencyKey(t *testing.T) {
	now := time.Date(2026, 10, 3, 2, 0, 0, 0, time.UTC)
	repository := newAPIRepository(t, now)
	handler := newAPIHandler(t, repository, now)
	token := loginAPI(t, handler)
	body := map[string]string{"current_password": "password", "new_password": "12345678"}
	missing := request(t, handler, http.MethodPut, "/api/v1/customer/password", token, body, nil)
	if missing.Code != http.StatusBadRequest {
		t.Fatalf("missing key status = %d, body=%s", missing.Code, missing.Body.String())
	}
	changed := request(t, handler, http.MethodPut, "/api/v1/customer/password", token, body, map[string]string{"Idempotency-Key": "password-change-1"})
	if changed.Code != http.StatusOK || !strings.Contains(changed.Body.String(), `"replayed":false`) {
		t.Fatalf("change status = %d, body=%s", changed.Code, changed.Body.String())
	}
	replayed := request(t, handler, http.MethodPut, "/api/v1/customer/password", token, body, map[string]string{"Idempotency-Key": "password-change-1"})
	if replayed.Code != http.StatusOK || !strings.Contains(replayed.Body.String(), `"replayed":true`) {
		t.Fatalf("replay status = %d, body=%s", replayed.Code, replayed.Body.String())
	}
}
