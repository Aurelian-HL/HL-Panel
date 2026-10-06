package customeridentity

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/faults"
)

var testFingerprintKey = []byte("0123456789abcdef0123456789abcdef")

type fakeRepository struct {
	records       map[string]CustomerRecord
	usernames     map[string]string
	sessions      map[string]Session
	rules         map[string][]RuleRecord
	usage         map[string]UsageRecord
	subscriptions map[string][]SubscriptionRecord
	passwordOps   map[string]string
	events        []audit.Event
	portal        PortalView
}

func newFakeRepository(t *testing.T, now time.Time) *fakeRepository {
	t.Helper()
	hash, err := auth.HashPassword("old-password")
	if err != nil {
		t.Fatal(err)
	}
	customer := customers.Customer{
		ID: "cus_1", Username: "alice", DisplayName: "Alice", UserGroupID: "ug_1", PasswordHash: hash,
		TrafficUsedBytes: 6 << 30, TrafficLimitBytes: 10 << 30, MaxRules: 8, SpeedLimitMbps: 200,
		IPLimit: 3, ConnectionLimit: 20, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour), Revision: 1,
	}
	return &fakeRepository{
		records:   map[string]CustomerRecord{"cus_1": {Customer: customer, UserGroupName: "默认用户组"}},
		usernames: map[string]string{"alice": "cus_1"}, sessions: map[string]Session{}, rules: map[string][]RuleRecord{},
		usage:         map[string]UsageRecord{"cus_1": {CustomerID: "cus_1", TrafficUsedBytes: 6 << 30, TrafficLimitBytes: 10 << 30, UpdatedAt: now}},
		subscriptions: map[string][]SubscriptionRecord{}, passwordOps: map[string]string{},
		portal: PortalView{SiteName: "XZPanel", PanelVersion: "0.1.0", Announcement: AnnouncementView{Title: "站点公告", Content: "服务正常"}},
	}
}

func (r *fakeRepository) CustomerByUsername(_ context.Context, username string) (CustomerRecord, error) {
	id, ok := r.usernames[username]
	if !ok {
		return CustomerRecord{}, faults.ErrNotFound
	}
	return cloneCustomerRecord(r.records[id]), nil
}

func (r *fakeRepository) CustomerByID(_ context.Context, id string) (CustomerRecord, error) {
	record, ok := r.records[id]
	if !ok {
		return CustomerRecord{}, faults.ErrNotFound
	}
	return cloneCustomerRecord(record), nil
}

func (r *fakeRepository) CreateSession(_ context.Context, session Session, event audit.Event) error {
	r.sessions[session.TokenHash] = session
	r.events = append(r.events, event)
	return nil
}

func (r *fakeRepository) SessionByTokenHash(_ context.Context, hash string, now time.Time) (Session, error) {
	session, ok := r.sessions[hash]
	if !ok || !now.Before(session.ExpiresAt) {
		return Session{}, faults.ErrUnauthorized
	}
	return session, nil
}

func (r *fakeRepository) RevokeSession(_ context.Context, hash, customerID string, event audit.Event) error {
	session, ok := r.sessions[hash]
	if !ok || session.CustomerID != customerID {
		return faults.ErrUnauthorized
	}
	delete(r.sessions, hash)
	r.events = append(r.events, event)
	return nil
}

func (r *fakeRepository) PasswordChangeReplay(_ context.Context, customerID, key, fingerprint string) (bool, error) {
	stored, ok := r.passwordOps[customerID+"\x00"+key]
	if !ok {
		return false, nil
	}
	if stored != fingerprint {
		return false, faults.ErrIdempotencyConflict
	}
	return true, nil
}

func (r *fakeRepository) ChangePassword(_ context.Context, input ChangePasswordInput, event audit.Event) (bool, error) {
	key := input.CustomerID + "\x00" + input.IdempotencyKey
	if fingerprint, ok := r.passwordOps[key]; ok {
		if fingerprint != input.RequestHMACSHA256 {
			return false, faults.ErrIdempotencyConflict
		}
		return true, nil
	}
	record, ok := r.records[input.CustomerID]
	if !ok {
		return false, faults.ErrNotFound
	}
	if record.Customer.Revision != input.ExpectedRevision {
		return false, faults.ErrConflict
	}
	record.Customer.PasswordHash = append([]byte(nil), input.PasswordHash...)
	record.Customer.Revision++
	record.Customer.UpdatedAt = input.UpdatedAt
	r.records[input.CustomerID] = record
	for hash, session := range r.sessions {
		if session.CustomerID == input.CustomerID && hash != input.KeepSessionHash {
			delete(r.sessions, hash)
		}
	}
	r.passwordOps[key] = input.RequestHMACSHA256
	r.events = append(r.events, event)
	return false, nil
}

func (r *fakeRepository) ListRulesByCustomer(_ context.Context, customerID string) ([]RuleRecord, error) {
	return append([]RuleRecord(nil), r.rules[customerID]...), nil
}

func (r *fakeRepository) RuleOptionsByCustomer(context.Context, string) (RuleOptionsView, error) {
	return RuleOptionsView{}, nil
}

func (r *fakeRepository) CustomerUsage(_ context.Context, customerID string) (UsageRecord, error) {
	value, ok := r.usage[customerID]
	if !ok {
		return UsageRecord{}, faults.ErrNotFound
	}
	return value, nil
}

func (r *fakeRepository) ListSubscriptionsByCustomer(_ context.Context, customerID string) ([]SubscriptionRecord, error) {
	return append([]SubscriptionRecord(nil), r.subscriptions[customerID]...), nil
}

func (r *fakeRepository) Portal(context.Context) (PortalView, error) { return r.portal, nil }

func (r *fakeRepository) AppendAudit(_ context.Context, event audit.Event) error {
	r.events = append(r.events, event)
	return nil
}

func cloneCustomerRecord(record CustomerRecord) CustomerRecord {
	record.Customer.PasswordHash = append([]byte(nil), record.Customer.PasswordHash...)
	return record
}

func newTestService(t *testing.T, repository Repository, now time.Time) *Service {
	t.Helper()
	service, err := NewService(repository, func() time.Time { return now }, Options{SessionTTL: time.Hour, PasswordFingerprintKey: testFingerprintKey})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func login(t *testing.T, service *Service) (LoginResult, Principal) {
	t.Helper()
	result, err := service.Login(context.Background(), "alice", "old-password")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := service.Authenticate(context.Background(), result.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	return result, principal
}

func TestLoginAndProfileNeverSerializePasswordMaterial(t *testing.T) {
	now := time.Date(2026, 10, 3, 1, 0, 0, 0, time.UTC)
	repository := newFakeRepository(t, now)
	service := newTestService(t, repository, now)
	result, principal := login(t, service)
	if result.User.Username != "alice" || result.User.UserGroupName != "默认用户组" || result.User.TrafficUsedBytes != 6<<30 {
		t.Fatalf("unexpected profile: %#v", result.User)
	}
	view, err := service.Me(context.Background(), principal)
	if err != nil || view.ID != "cus_1" {
		t.Fatalf("Me() = %#v, %v", view, err)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	encoded := strings.ToLower(string(raw))
	if strings.Contains(encoded, "password") || strings.Contains(encoded, "hash") || strings.Contains(encoded, "old-password") {
		t.Fatalf("login response leaked password material: %s", raw)
	}
}

func TestLoginRejectsDisabledCustomerButAllowsExpiredReadOnlyProfile(t *testing.T) {
	now := time.Date(2026, 10, 3, 1, 0, 0, 0, time.UTC)
	repository := newFakeRepository(t, now)
	record := repository.records["cus_1"]
	record.Customer.Disabled = true
	repository.records["cus_1"] = record
	service := newTestService(t, repository, now)
	if _, err := service.Login(context.Background(), "alice", "old-password"); !errors.Is(err, faults.ErrUnauthorized) {
		t.Fatalf("disabled login error = %v", err)
	}
	record.Customer.Disabled = false
	expired := now.Add(-time.Minute)
	record.Customer.ExpiresAt = &expired
	repository.records["cus_1"] = record
	result, err := service.Login(context.Background(), "alice", "old-password")
	if err != nil || result.User.EffectiveStatus != customers.StatusExpired {
		t.Fatalf("expired login = %#v, %v", result, err)
	}
	// A session issued before an administrator disables the account must stop
	// authenticating immediately; relying only on the session TTL would leave a
	// disabled customer usable for hours.
	repository.records["cus_1"] = func() CustomerRecord {
		current := repository.records["cus_1"]
		current.Customer.ExpiresAt = nil
		return current
	}()
	active, principal := login(t, service)
	if active.AccessToken == "" {
		t.Fatal("active customer login returned an empty token")
	}
	current := repository.records["cus_1"]
	current.Customer.Disabled = true
	repository.records["cus_1"] = current
	if _, err := service.Authenticate(context.Background(), active.AccessToken); !errors.Is(err, faults.ErrUnauthorized) {
		t.Fatalf("disabled customer session remained valid: %v", err)
	}
	if _, err := service.Me(context.Background(), principal); !errors.Is(err, faults.ErrUnauthorized) {
		t.Fatalf("disabled customer profile remained readable: %v", err)
	}
}

func TestOwnRulesAndSubscriptionsFailClosedOnForeignProjection(t *testing.T) {
	now := time.Date(2026, 10, 3, 1, 0, 0, 0, time.UTC)
	repository := newFakeRepository(t, now)
	service := newTestService(t, repository, now)
	_, principal := login(t, service)
	repository.rules["cus_1"] = []RuleRecord{{CustomerID: "cus_other", ID: "rule_foreign"}}
	if _, err := service.Rules(context.Background(), principal); err == nil {
		t.Fatal("foreign rule projection was accepted")
	}
	repository.rules["cus_1"] = nil
	repository.subscriptions["cus_1"] = []SubscriptionRecord{{CustomerID: "cus_other", ID: "sub_foreign"}}
	if _, err := service.Subscriptions(context.Background(), principal); err == nil {
		t.Fatal("foreign subscription projection was accepted")
	}
}

func TestRulesExposeCustomerIngressProtocolSeparatelyFromTransport(t *testing.T) {
	now := time.Date(2026, 10, 3, 1, 0, 0, 0, time.UTC)
	repository := newFakeRepository(t, now)
	repository.rules["cus_1"] = []RuleRecord{
		{CustomerID: "cus_1", ID: "tcp", Name: "NY TCP", IngressProtocol: "tcp", Protocol: "tcp"},
		{CustomerID: "cus_1", ID: "socks5", Name: "NY SOCKS5", IngressProtocol: "socks5", Protocol: "tcp"},
		{CustomerID: "cus_1", ID: "vless", Name: "VLESS", IngressProtocol: "vless_reality", Protocol: "tcp"},
	}
	service := newTestService(t, repository, now)
	_, principal := login(t, service)
	views, err := service.Rules(context.Background(), principal)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 3 || views[0].IngressProtocol == views[1].IngressProtocol || views[1].IngressProtocol != "socks5" || views[2].IngressProtocol != "vless_reality" {
		t.Fatalf("customer ingress protocol projection = %#v", views)
	}
}

func TestSubscriptionURIRequiresReadyStateAndApprovedScheme(t *testing.T) {
	now := time.Date(2026, 10, 3, 1, 0, 0, 0, time.UTC)
	repository := newFakeRepository(t, now)
	repository.subscriptions["cus_1"] = []SubscriptionRecord{
		{CustomerID: "cus_1", ID: "pending", Status: "pending_activation", URI: "vless://secret@example.com:443"},
		{CustomerID: "cus_1", ID: "invalid", Status: "active", Ready: true, URI: "https://secret@example.com"},
		{CustomerID: "cus_1", ID: "ready", Status: "active", Ready: true, URI: "vless://uuid@example.com:443?security=tls"},
	}
	service := newTestService(t, repository, now)
	_, principal := login(t, service)
	items, err := service.Subscriptions(context.Background(), principal)
	if err != nil {
		t.Fatal(err)
	}
	if items[0].URI != "" || items[1].URI != "" || !strings.HasPrefix(items[2].URI, "vless://") {
		t.Fatalf("subscription URI filtering failed: %#v", items)
	}
}

func TestPasswordChangeIsIdempotentAndKeepsCurrentSession(t *testing.T) {
	now := time.Date(2026, 10, 3, 1, 0, 0, 0, time.UTC)
	repository := newFakeRepository(t, now)
	service := newTestService(t, repository, now)
	result, principal := login(t, service)
	replayed, err := service.ChangePassword(context.Background(), principal, "old-password", "new-password", "change-1")
	if err != nil || replayed {
		t.Fatalf("first change = %v, %v", replayed, err)
	}
	replayed, err = service.ChangePassword(context.Background(), principal, "old-password", "new-password", "change-1")
	if err != nil || !replayed {
		t.Fatalf("replay change = %v, %v", replayed, err)
	}
	if _, err := service.ChangePassword(context.Background(), principal, "old-password", "different", "change-1"); !errors.Is(err, faults.ErrIdempotencyConflict) {
		t.Fatalf("changed replay error = %v", err)
	}
	if _, err := service.Authenticate(context.Background(), result.AccessToken); err != nil {
		t.Fatalf("current session was revoked: %v", err)
	}
	if _, err := service.Login(context.Background(), "alice", "old-password"); !errors.Is(err, faults.ErrUnauthorized) {
		t.Fatalf("old password login error = %v", err)
	}
	if _, err := service.Login(context.Background(), "alice", "new-password"); err != nil {
		t.Fatalf("new password login failed: %v", err)
	}
}

func TestUsageCalculatesRemainingWithoutNegativeValues(t *testing.T) {
	now := time.Date(2026, 10, 3, 1, 0, 0, 0, time.UTC)
	repository := newFakeRepository(t, now)
	service := newTestService(t, repository, now)
	_, principal := login(t, service)
	repository.usage["cus_1"] = UsageRecord{CustomerID: "cus_1", TrafficUsedBytes: 12, TrafficLimitBytes: 10, UpdatedAt: now}
	view, err := service.Usage(context.Background(), principal)
	if err != nil || view.RemainingBytes == nil || *view.RemainingBytes != 0 {
		t.Fatalf("usage = %#v, %v", view, err)
	}
	repository.usage["cus_1"] = UsageRecord{CustomerID: "cus_1", TrafficUsedBytes: 12, TrafficLimitBytes: 0, UpdatedAt: now}
	view, err = service.Usage(context.Background(), principal)
	if err != nil || view.RemainingBytes != nil {
		t.Fatalf("unlimited usage = %#v, %v", view, err)
	}
}
