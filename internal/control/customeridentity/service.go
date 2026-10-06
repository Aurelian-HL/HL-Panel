package customeridentity

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/idgen"
	"github.com/hongle/hl-panel/internal/securetoken"
)

const (
	defaultSessionTTL = 8 * time.Hour
	maxPasswordBytes  = 4096
)

type Options struct {
	SessionTTL             time.Duration
	PasswordFingerprintKey []byte
}

type Service struct {
	repository             Repository
	now                    func() time.Time
	sessionTTL             time.Duration
	passwordFingerprintKey []byte
}

func NewService(repository Repository, now func() time.Time, options Options) (*Service, error) {
	if repository == nil {
		return nil, errors.New("customer identity repository is required")
	}
	if now == nil {
		now = time.Now
	}
	if options.SessionTTL <= 0 {
		options.SessionTTL = defaultSessionTTL
	}
	if len(options.PasswordFingerprintKey) < 32 {
		return nil, errors.New("customer password fingerprint key must contain at least 32 bytes")
	}
	return &Service{
		repository:             repository,
		now:                    now,
		sessionTTL:             options.SessionTTL,
		passwordFingerprintKey: append([]byte(nil), options.PasswordFingerprintKey...),
	}, nil
}

func (s *Service) Login(ctx context.Context, username, password string) (LoginResult, error) {
	username = strings.TrimSpace(username)
	if username == "" || len(username) > 128 || password == "" || len(password) > maxPasswordBytes {
		s.recordFailedLogin(ctx, username)
		return LoginResult{}, faults.ErrUnauthorized
	}
	record, err := s.repository.CustomerByUsername(ctx, username)
	if err != nil || auth.VerifyPassword(record.Customer.PasswordHash, password) != nil || record.Customer.Disabled {
		s.recordFailedLogin(ctx, username)
		return LoginResult{}, faults.ErrUnauthorized
	}
	rawToken, err := securetoken.Generate("cus")
	if err != nil {
		return LoginResult{}, err
	}
	sessionID, err := idgen.New("cses")
	if err != nil {
		return LoginResult{}, err
	}
	now := s.now().UTC()
	session := Session{ID: sessionID, CustomerID: record.Customer.ID, TokenHash: securetoken.Hash(rawToken), ExpiresAt: now.Add(s.sessionTTL), CreatedAt: now}
	event, err := audit.NewEvent(now, "customer", record.Customer.ID, "customer_auth.login", "customer_session", session.ID, "succeeded", nil)
	if err != nil {
		return LoginResult{}, err
	}
	if err := s.repository.CreateSession(ctx, session, event); err != nil {
		return LoginResult{}, err
	}
	return LoginResult{AccessToken: rawToken, ExpiresAt: session.ExpiresAt, User: profile(record, now)}, nil
}

func (s *Service) Authenticate(ctx context.Context, rawToken string) (Principal, error) {
	rawToken = strings.TrimSpace(rawToken)
	if rawToken == "" {
		return Principal{}, faults.ErrUnauthorized
	}
	hash := securetoken.Hash(rawToken)
	session, err := s.repository.SessionByTokenHash(ctx, hash, s.now().UTC())
	if err != nil {
		return Principal{}, faults.ErrUnauthorized
	}
	record, err := s.repository.CustomerByID(ctx, session.CustomerID)
	if err != nil || record.Customer.ID != session.CustomerID || record.Customer.Disabled {
		return Principal{}, faults.ErrUnauthorized
	}
	return Principal{CustomerID: session.CustomerID, SessionID: session.ID, SessionTokenHash: hash}, nil
}

func (s *Service) Logout(ctx context.Context, principal Principal) error {
	if !validPrincipal(principal) {
		return faults.ErrUnauthorized
	}
	now := s.now().UTC()
	event, err := audit.NewEvent(now, "customer", principal.CustomerID, "customer_auth.logout", "customer_session", principal.SessionID, "succeeded", nil)
	if err != nil {
		return err
	}
	return s.repository.RevokeSession(ctx, principal.SessionTokenHash, principal.CustomerID, event)

}

func (s *Service) Me(ctx context.Context, principal Principal) (Profile, error) {
	record, err := s.ownCustomer(ctx, principal)
	if err != nil {
		return Profile{}, err
	}
	return profile(record, s.now().UTC()), nil
}

func (s *Service) Rules(ctx context.Context, principal Principal) ([]RuleView, error) {
	if _, err := s.ownCustomer(ctx, principal); err != nil {
		return nil, err
	}
	records, err := s.repository.ListRulesByCustomer(ctx, principal.CustomerID)
	if err != nil {
		return nil, err
	}
	views := make([]RuleView, 0, len(records))
	for _, record := range records {
		if record.CustomerID != principal.CustomerID {
			return nil, errors.New("customer rule repository returned a foreign record")
		}
		views = append(views, RuleView{
			ID: record.ID, Name: record.Name, IngressProtocol: record.IngressProtocol, Protocol: record.Protocol, ConnectHost: record.ConnectHost,
			ListenPort: record.ListenPort, RouteDescription: record.RouteDescription,
			Targets: append([]TargetView(nil), record.Targets...), Paused: record.Paused, Status: record.Status, Deployed: record.Deployed, Revision: record.Revision,
		})
	}
	return views, nil
}

func (s *Service) RuleOptions(ctx context.Context, principal Principal) (RuleOptionsView, error) {
	if _, err := s.ownCustomer(ctx, principal); err != nil {
		return RuleOptionsView{}, err
	}
	return s.repository.RuleOptionsByCustomer(ctx, principal.CustomerID)
}

func (s *Service) Usage(ctx context.Context, principal Principal) (UsageView, error) {
	if _, err := s.ownCustomer(ctx, principal); err != nil {
		return UsageView{}, err
	}
	record, err := s.repository.CustomerUsage(ctx, principal.CustomerID)
	if err != nil {
		return UsageView{}, err
	}
	if record.CustomerID != principal.CustomerID || record.TrafficUsedBytes < 0 || record.TrafficLimitBytes < 0 {
		return UsageView{}, errors.New("customer usage repository returned an invalid record")
	}
	var remaining *int64
	if record.TrafficLimitBytes > 0 {
		value := max(record.TrafficLimitBytes-record.TrafficUsedBytes, 0)
		remaining = &value
	}
	return UsageView{TrafficUsedBytes: record.TrafficUsedBytes, TrafficLimitBytes: record.TrafficLimitBytes, RemainingBytes: remaining, UpdatedAt: record.UpdatedAt}, nil
}

func (s *Service) Subscriptions(ctx context.Context, principal Principal) ([]SubscriptionView, error) {
	if _, err := s.ownCustomer(ctx, principal); err != nil {
		return nil, err
	}
	records, err := s.repository.ListSubscriptionsByCustomer(ctx, principal.CustomerID)
	if err != nil {
		return nil, err
	}
	views := make([]SubscriptionView, 0, len(records))
	for _, record := range records {
		if record.CustomerID != principal.CustomerID {
			return nil, errors.New("customer subscription repository returned a foreign record")
		}
		view := SubscriptionView{ID: record.ID, RuleID: record.RuleID, Name: record.Name, Protocol: record.Protocol, Endpoint: record.Endpoint, Status: record.Status}
		if record.Ready && validSubscriptionURI(record.URI) {
			view.URI = record.URI
		}
		views = append(views, view)
	}
	return views, nil
}

func (s *Service) Portal(ctx context.Context) (PortalView, error) {
	view, err := s.repository.Portal(ctx)
	if err != nil {
		return PortalView{}, err
	}
	view.SiteName = strings.TrimSpace(view.SiteName)
	view.PanelVersion = strings.TrimSpace(view.PanelVersion)
	if view.SiteName == "" || len(view.SiteName) > 128 || len(view.PanelVersion) > 128 || len(view.Announcement.Title) > 200 || len(view.Announcement.Content) > 20_000 {
		return PortalView{}, errors.New("customer portal repository returned invalid public content")
	}
	if len(view.SiteInfo) > 100 || len(view.BackendInfo) > 100 {
		return PortalView{}, errors.New("customer portal repository returned too many information items")
	}
	return view, nil
}

func (s *Service) ChangePassword(ctx context.Context, principal Principal, currentPassword, newPassword, idempotencyKey string) (bool, error) {
	if !validPrincipal(principal) {
		return false, faults.ErrUnauthorized
	}
	if currentPassword == "" || newPassword == "" || len(currentPassword) > maxPasswordBytes || len(newPassword) > maxPasswordBytes {
		return false, fmt.Errorf("%w: current and new passwords are required and bounded", faults.ErrValidation)
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if !validIdempotencyKey(idempotencyKey) {
		return false, fmt.Errorf("%w: Idempotency-Key must contain 1 to 128 non-space characters", faults.ErrValidation)
	}
	fingerprint := s.passwordFingerprint(principal.CustomerID, currentPassword, newPassword)
	replayed, err := s.repository.PasswordChangeReplay(ctx, principal.CustomerID, idempotencyKey, fingerprint)
	if err != nil || replayed {
		return replayed, err
	}
	record, err := s.ownCustomer(ctx, principal)
	if err != nil {
		return false, err
	}
	if auth.VerifyPassword(record.Customer.PasswordHash, currentPassword) != nil {
		return false, faults.ErrUnauthorized
	}
	passwordHash, err := auth.HashPassword(newPassword)
	if err != nil {
		return false, err
	}
	now := s.now().UTC()
	event, err := audit.NewEvent(now, "customer", principal.CustomerID, "customer.password_change", "customer", principal.CustomerID, "succeeded", map[string]any{"revision": record.Customer.Revision + 1})
	if err != nil {
		return false, err
	}
	return s.repository.ChangePassword(ctx, ChangePasswordInput{
		CustomerID: principal.CustomerID, ExpectedRevision: record.Customer.Revision, PasswordHash: passwordHash,
		KeepSessionHash: principal.SessionTokenHash, IdempotencyKey: idempotencyKey, RequestHMACSHA256: fingerprint, UpdatedAt: now,
	}, event)
}

func (s *Service) ownCustomer(ctx context.Context, principal Principal) (CustomerRecord, error) {
	if !validPrincipal(principal) {
		return CustomerRecord{}, faults.ErrUnauthorized
	}
	record, err := s.repository.CustomerByID(ctx, principal.CustomerID)
	if err != nil {
		return CustomerRecord{}, err
	}
	if record.Customer.ID != principal.CustomerID {
		return CustomerRecord{}, errors.New("customer identity repository returned a foreign customer")
	}
	if record.Customer.Disabled {
		return CustomerRecord{}, faults.ErrUnauthorized
	}
	return record, nil
}

func (s *Service) passwordFingerprint(customerID, currentPassword, newPassword string) string {
	mac := hmac.New(sha256.New, s.passwordFingerprintKey)
	_, _ = mac.Write([]byte(customerID))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(currentPassword))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(newPassword))
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *Service) recordFailedLogin(ctx context.Context, username string) {
	event, err := audit.NewEvent(s.now().UTC(), "customer", username, "customer_auth.login", "customer_session", "", "failed", nil)
	if err == nil {
		_ = s.repository.AppendAudit(ctx, event)
	}
}

func profile(record CustomerRecord, now time.Time) Profile {
	customer := record.Customer
	return Profile{
		ID: customer.ID, Username: customer.Username, DisplayName: customer.DisplayName, UserType: "user",
		UserGroupName: record.UserGroupName, ExpiresAt: cloneTime(customer.ExpiresAt), TrafficUsedBytes: customer.TrafficUsedBytes,
		TrafficLimitBytes: customer.TrafficLimitBytes, MaxRules: customer.MaxRules, SpeedLimitMbps: customer.SpeedLimitMbps,
		IPLimit: customer.IPLimit, ConnectionLimit: customer.ConnectionLimit, EffectiveStatus: customer.EffectiveStatus(now),
	}
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func validPrincipal(principal Principal) bool {
	return principal.CustomerID != "" && principal.SessionID != "" && len(principal.SessionTokenHash) == 64
}

func validIdempotencyKey(value string) bool {
	return value != "" && len(value) <= 128 && strings.IndexFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) < 0
}

func validSubscriptionURI(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User == nil || parsed.Host == "" {
		return false
	}
	scheme := strings.ToLower(parsed.Scheme)
	return scheme == "vless" || scheme == "socks5"
}
