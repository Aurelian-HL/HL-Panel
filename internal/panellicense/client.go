package panellicense

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"
)

type Config struct {
	IssuerURL  string
	IssuerID   string
	Keys       map[string]ed25519.PublicKey
	Domain     string
	InstanceID string
	Directory  string
	Timeout    time.Duration
	Now        func() time.Time
}

type cachedState struct {
	Envelope   *Envelope         `json:"envelope,omitempty"`
	Challenge  *Envelope         `json:"challenge,omitempty"`
	Dismissals map[string]string `json:"dismissals,omitempty"`
}

type Client struct {
	config             Config
	key                ed25519.PrivateKey
	http               *http.Client
	mu                 sync.RWMutex
	flight             sync.Mutex
	state              cachedState
	claims             *Claims
	challenge          *Challenge
	lastError          string
	lastAttempt        time.Time
	configurationError error
}

var ErrSyncThrottled = errors.New("授权同步过于频繁，请在 10 秒后重试")
var ErrSyncBusy = errors.New("授权校验正在进行，请稍后重试")

// RestrictedClient keeps the control plane serving existing traffic when an
// older installer has not provisioned v2 identity yet. It grants no management
// permission and cannot activate or make unsigned requests.
func RestrictedClient() *Client {
	err := errors.New("授权配置待接入，请使用发行包内 setup-panel-license.sh 配置管理域名和本机身份后重启面板；现有转发继续运行")
	return &Client{config: Config{Now: time.Now}, configurationError: err, lastError: err.Error()}
}

type Status struct {
	Enabled           bool       `json:"enabled"`
	Status            string     `json:"status"`
	ManagementAllowed bool       `json:"management_allowed"`
	Domain            string     `json:"domain"`
	LicenseID         string     `json:"license_id,omitempty"`
	Revision          int64      `json:"revision,omitempty"`
	Term              *Term      `json:"term,omitempty"`
	ExpiresAt         *time.Time `json:"expires_at"`
	CheckedAt         *time.Time `json:"checked_at,omitempty"`
	LeaseValidUntil   *time.Time `json:"lease_valid_until,omitempty"`
	LastError         string     `json:"last_error,omitempty"`
	ReminderDue       bool       `json:"reminder_due"`
	RenewalURL        string     `json:"renewal_url"`
}

// ValidateConfig checks trust and identity without creating state or keys.
func ValidateConfig(config Config) error {
	u, err := url.Parse(config.IssuerURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("invalid license issuer URL")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && ip != nil && ip.IsLoopback()) {
		return errors.New("license issuer requires HTTPS")
	}
	_, err = NormalizeDomain(config.Domain)
	if err != nil || config.IssuerID == "" || len(config.Keys) == 0 || len(config.InstanceID) < 8 || len(config.InstanceID) > 128 || strings.ContainsAny(config.InstanceID, " \t\r\n") || !filepath.IsAbs(config.Directory) {
		return errors.New("incomplete signed license configuration")
	}
	issuer, err := url.Parse(config.IssuerID)
	if err != nil || issuer.Scheme != "https" || issuer.Host == "" || issuer.User != nil || issuer.RawQuery != "" || issuer.Fragment != "" {
		return errors.New("invalid issuer identity")
	}
	for id, key := range config.Keys {
		if len(key) != ed25519.PublicKeySize || len(id) == 0 || len(id) > 100 || strings.ContainsAny(id, " \t\r\n") {
			return errors.New("invalid issuer public key")
		}
	}
	return nil
}

func NewClient(config Config) (*Client, error) {
	if err := ValidateConfig(config); err != nil {
		return nil, err
	}
	domain, _ := NormalizeDomain(config.Domain)
	config.Domain = domain
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.Timeout <= 0 {
		config.Timeout = 10 * time.Second
	}
	if err := os.MkdirAll(config.Directory, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(config.Directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("invalid license state directory")
	}
	keyPath := filepath.Join(config.Directory, "instance-key")
	raw, err := readPrivateFile(keyPath)
	if errors.Is(err, os.ErrNotExist) {
		_, key, e := ed25519.GenerateKey(rand.Reader)
		if e != nil {
			return nil, e
		}
		f, e := os.OpenFile(keyPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return nil, e
		}
		_, e = f.Write(key)
		closeErr := f.Close()
		if e != nil {
			return nil, e
		}
		if closeErr != nil {
			return nil, closeErr
		}
		raw = key
	} else if err != nil {
		return nil, err
	}
	if len(raw) != ed25519.PrivateKeySize || !bytes.Equal(raw, ed25519.NewKeyFromSeed(raw[:ed25519.SeedSize])) {
		return nil, errors.New("invalid instance private key")
	}
	c := &Client{config: config, key: ed25519.PrivateKey(raw), http: &http.Client{Timeout: config.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("license redirects are forbidden") }}}
	c.state.Dismissals = make(map[string]string)
	data, err := readPrivateFile(filepath.Join(config.Directory, "state.json"))
	if err == nil {
		if err := c.restore(data); err != nil {
			// A damaged cache must fail closed for management without preventing
			// existing subscriptions and authenticated node reports from serving.
			c.state = cachedState{Dismissals: make(map[string]string)}
			c.claims, c.challenge = nil, nil
			c.lastError = "授权缓存校验失败，请重新激活；现有转发继续运行"
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return c, nil
}

func (c *Client) restore(data []byte) error {
	if err := json.Unmarshal(data, &c.state); err != nil {
		return err
	}
	if c.state.Envelope != nil {
		var claims Claims
		if err := Verify(*c.state.Envelope, c.config.Keys, &claims); err != nil {
			return err
		}
		if err := c.validateClaims(claims, "", false); err != nil {
			return err
		}
		c.claims = &claims
	}
	if c.state.Challenge != nil {
		var challenge Challenge
		if err := Verify(*c.state.Challenge, c.config.Keys, &challenge); err != nil {
			return err
		}
		if !c.identity(challenge.Version, challenge.Issuer, challenge.Product, challenge.Audience, challenge.Domain, challenge.InstanceID, challenge.InstanceKeyThumbprint) {
			return errors.New("challenge identity mismatch")
		}
		c.challenge = &challenge
	}
	if c.state.Dismissals == nil {
		c.state.Dismissals = make(map[string]string)
	}
	return nil
}

func readPrivateFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 131072 {
		return nil, errors.New("invalid license state file")
	}
	return os.ReadFile(path)
}
func (c *Client) identity(version int, issuer, product, audience, domain, instance, thumb string) bool {
	return version == Version && issuer == c.config.IssuerID && product == Product && audience == Audience && domain == c.config.Domain && instance == c.config.InstanceID && thumb == Thumbprint(c.key.Public().(ed25519.PublicKey))
}
func (c *Client) validateClaims(s Claims, requestID string, fresh bool) error {
	if !c.identity(s.Version, s.Issuer, s.Product, s.Audience, s.Domain, s.InstanceID, s.InstanceKeyThumbprint) || s.LicenseID == "" || s.Revision < 1 || s.Term.Validate() != nil {
		return errors.New("license identity mismatch")
	}
	if s.Status != "active" && s.Status != "expired" && s.Status != "revoked" {
		return errors.New("invalid license status")
	}
	if s.ActivatedAt.IsZero() || s.IssuedAt.Before(s.ActivatedAt) || !s.RefreshAfter.After(s.IssuedAt) || !s.LeaseValidUntil.After(s.RefreshAfter) || s.LeaseValidUntil.Sub(s.IssuedAt) > 24*time.Hour+time.Minute || (s.Term.Kind == "perpetual") != (s.LicenseExpiresAt == nil) {
		return errors.New("invalid license dates")
	}
	if s.LicenseExpiresAt != nil && (s.LicenseExpiresAt.IsZero() || s.Status == "active" && !s.LicenseExpiresAt.After(s.IssuedAt) || s.Status == "expired" && s.LicenseExpiresAt.After(s.IssuedAt)) {
		return errors.New("inconsistent license expiry")
	}
	if s.Status == "active" && s.LicenseExpiresAt != nil && s.LeaseValidUntil.After(*s.LicenseExpiresAt) {
		return errors.New("license lease exceeds commercial expiry")
	}
	if fresh && (s.RequestID != requestID || s.IssuedAt.Before(c.config.Now().Add(-5*time.Minute)) || s.IssuedAt.After(c.config.Now().Add(5*time.Minute))) {
		return errors.New("license response nonce or clock mismatch")
	}
	return nil
}
func (c *Client) request(operation string) Request {
	var nonce [24]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		panic("cryptographic random unavailable")
	}
	return Request{Version: Version, Product: Product, Audience: Audience, RequestID: base64.RawURLEncoding.EncodeToString(nonce[:]), IssuedAt: c.config.Now().UTC(), Domain: c.config.Domain, InstanceID: c.config.InstanceID, InstancePublicKey: base64.RawURLEncoding.EncodeToString(c.key.Public().(ed25519.PublicKey))}
}
func (c *Client) call(ctx context.Context, operation, path string, r Request) (Envelope, error) {
	signed, err := SignRequest(operation, r, c.key)
	if err != nil {
		return Envelope{}, err
	}
	raw, _ := json.Marshal(signed)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.config.IssuerURL, "/")+PublicPath+path, bytes.NewReader(raw))
	if err != nil {
		return Envelope{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(req)
	if err != nil {
		return Envelope{}, errors.New("授权服务暂时无法连接，将继续重试")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Envelope{}, errors.New("授权服务未接受请求，请检查授权码、DNS 证明或实例绑定")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 131073))
	if err != nil || len(data) > 131072 {
		return Envelope{}, errors.New("invalid issuer response")
	}
	var envelope Envelope
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return Envelope{}, errors.New("invalid issuer response")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return Envelope{}, errors.New("invalid issuer response")
	}
	return envelope, nil
}
func (c *Client) saveLocked() error {
	data, err := json.Marshal(c.state)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(c.config.Directory, ".license-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(name, filepath.Join(c.config.Directory, "state.json"))
}
func (c *Client) accept(envelope Envelope, requestID string) error {
	var s Claims
	if err := Verify(envelope, c.config.Keys, &s); err != nil {
		return err
	}
	if err := c.validateClaims(s, requestID, true); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if old := c.claims; old != nil {
		if old.LicenseID != s.LicenseID || s.Revision < old.Revision || s.IssuedAt.Before(old.IssuedAt) {
			return errors.New("outdated license response")
		}
		if s.Revision == old.Revision {
			a, b := *old, s
			a.RequestID = ""
			b.RequestID = ""
			a.IssuedAt = time.Time{}
			b.IssuedAt = time.Time{}
			a.RefreshAfter = time.Time{}
			b.RefreshAfter = time.Time{}
			a.LeaseValidUntil = time.Time{}
			b.LeaseValidUntil = time.Time{}
			// Natural expiry changes derived status without a commercial revision.
			if a.Status == "active" && b.Status == "expired" {
				a.Status = "expired"
			}
			if !reflect.DeepEqual(a, b) {
				return errors.New("conflicting license revision")
			}
		}
	}
	previous := c.state
	c.state.Envelope = &envelope
	c.state.Challenge = nil
	if err := c.saveLocked(); err != nil {
		c.state = previous
		return err
	}
	c.claims = &s
	c.challenge = nil
	c.lastError = ""
	return nil
}
func (c *Client) Prepare(ctx context.Context, code string) (Challenge, error) {
	if c.configurationError != nil {
		return Challenge{}, c.configurationError
	}
	c.flight.Lock()
	defer c.flight.Unlock()
	r := c.request("prepare")
	r.Code = strings.TrimSpace(code)
	if len(r.Code) < 16 || len(r.Code) > 512 || strings.ContainsAny(r.Code, " \t\r\n") {
		return Challenge{}, errors.New("授权码格式无效")
	}
	envelope, err := c.call(ctx, "prepare", "/activation/prepare", r)
	if err != nil {
		return Challenge{}, err
	}
	var challenge Challenge
	if err := Verify(envelope, c.config.Keys, &challenge); err != nil {
		return Challenge{}, err
	}
	if !c.identity(challenge.Version, challenge.Issuer, challenge.Product, challenge.Audience, challenge.Domain, challenge.InstanceID, challenge.InstanceKeyThumbprint) || challenge.RequestID != r.RequestID || challenge.DNSName != "_hl-license."+c.config.Domain || !strings.HasPrefix(challenge.DNSValue, "hl-license-v2=") || challenge.ReservationID == "" || challenge.IssuedAt.Before(c.config.Now().Add(-5*time.Minute)) || challenge.IssuedAt.After(c.config.Now().Add(5*time.Minute)) || !challenge.ExpiresAt.After(c.config.Now()) || challenge.ExpiresAt.Sub(challenge.IssuedAt) > 15*time.Minute {
		return Challenge{}, errors.New("invalid signed activation challenge")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	previous := c.state
	c.state.Challenge = &envelope
	if err := c.saveLocked(); err != nil {
		c.state = previous
		return Challenge{}, err
	}
	c.challenge = &challenge
	return challenge, nil
}
func (c *Client) Commit(ctx context.Context) error {
	if c.configurationError != nil {
		return c.configurationError
	}
	c.flight.Lock()
	defer c.flight.Unlock()
	c.mu.RLock()
	challenge := c.challenge
	c.mu.RUnlock()
	if challenge == nil {
		return errors.New("请先提供授权码获取域名证明")
	}
	if !challenge.ExpiresAt.After(c.config.Now()) {
		return errors.New("域名证明已过期，请重新获取")
	}
	r := c.request("commit")
	r.ReservationID = challenge.ReservationID
	envelope, err := c.call(ctx, "commit", "/activation/commit", r)
	if err != nil {
		return err
	}
	return c.accept(envelope, r.RequestID)
}
func (c *Client) Sync(ctx context.Context) error {
	if c.configurationError != nil {
		return c.configurationError
	}
	if !c.flight.TryLock() {
		return ErrSyncBusy
	}
	defer c.flight.Unlock()
	c.mu.Lock()
	claims := c.claims
	if claims == nil {
		c.mu.Unlock()
		return errors.New("面板尚未激活授权")
	}
	if c.config.Now().Sub(c.lastAttempt) < 10*time.Second {
		c.mu.Unlock()
		return ErrSyncThrottled
	}
	c.lastAttempt = c.config.Now()
	licenseID := claims.LicenseID
	c.mu.Unlock()
	r := c.request("sync")
	r.LicenseID = licenseID
	envelope, err := c.call(ctx, "sync", "/sync", r)
	if err == nil {
		err = c.accept(envelope, r.RequestID)
	}
	if err != nil {
		c.mu.Lock()
		c.lastError = "授权同步失败，将自动重试；现有转发继续运行"
		c.mu.Unlock()
	}
	return err
}
func (c *Client) Run(ctx context.Context) {
	if c.configurationError != nil {
		<-ctx.Done()
		return
	}
	delay := time.Second
	for {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if err := c.Sync(ctx); err != nil {
			delay *= 2
			if delay < 15*time.Second {
				delay = 15 * time.Second
			}
			if delay > 5*time.Minute {
				delay = 5 * time.Minute
			}
		} else {
			c.mu.RLock()
			delay = c.claims.RefreshAfter.Sub(c.config.Now())
			c.mu.RUnlock()
			if delay < 10*time.Second {
				delay = 10 * time.Second
			}
			if delay > 30*time.Minute {
				delay = 30 * time.Minute
			}
		}
	}
}
func (c *Client) Status(actor string) Status {
	c.mu.RLock()
	defer c.mu.RUnlock()
	now := c.config.Now()
	s := Status{Enabled: true, Status: "unactivated", Domain: c.config.Domain, RenewalURL: "https://ht.hongle.work/", LastError: c.lastError}
	if c.configurationError != nil {
		s.Status = "unconfigured"
		return s
	}
	if a := c.claims; a != nil {
		s.Status = a.Status
		s.LicenseID = a.LicenseID
		s.Revision = a.Revision
		term := a.Term
		s.Term = &term
		s.ExpiresAt = a.LicenseExpiresAt
		checked := a.IssuedAt
		valid := a.LeaseValidUntil
		s.CheckedAt = &checked
		s.LeaseValidUntil = &valid
		if s.Status == "active" && a.LicenseExpiresAt != nil && !now.Before(*a.LicenseExpiresAt) {
			s.Status = "expired"
		}
		if s.Status == "active" && (!now.Before(a.LeaseValidUntil) || now.Before(a.IssuedAt.Add(-5*time.Minute))) {
			s.Status = "stale"
		}
		s.ManagementAllowed = s.Status == "active"
		if a.LicenseExpiresAt != nil && a.Status != "revoked" && a.LicenseExpiresAt.Sub(now) <= 72*time.Hour {
			s.ReminderDue = c.state.Dismissals[actor] != businessDay(now)
		}
	}
	return s
}
func businessDay(now time.Time) string {
	return now.In(time.FixedZone("Asia/Shanghai", 8*60*60)).Format("2006-01-02")
}
func (c *Client) Dismiss(actor string) error {
	if c.configurationError != nil {
		return c.configurationError
	}
	if actor == "" {
		return errors.New("administrator required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	previous := c.state.Dismissals[actor]
	c.state.Dismissals[actor] = businessDay(c.config.Now())
	if err := c.saveLocked(); err != nil {
		c.state.Dismissals[actor] = previous
		return err
	}
	return nil
}
