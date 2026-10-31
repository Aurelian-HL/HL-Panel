package panellicense

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// UserError contains only curated messages; never forward issuer response text,
// secrets, local filenames or raw network errors into the administration UI.
type UserError struct {
	Code    string
	Message string
}

func (e *UserError) Error() string { return e.Message }

var ErrRedemptionPending = &UserError{"redemption_pending", "有一笔兑换结果待确认，请先恢复兑换结果"}

// Older issuers can keep serving existing licenses before enabling the shop.
var ErrShopNotOpen = errors.New("license shop is not open")

func issuerError(response *http.Response) error {
	var body ErrorResponse
	data, err := io.ReadAll(io.LimitReader(response.Body, 8193))
	if err != nil || len(data) > 8192 || json.Unmarshal(data, &body) != nil {
		return &UserError{"license_service_unavailable", "授权服务响应未能确认，请稍后恢复兑换结果"}
	}
	messages := map[string]string{
		"invalid_code":          "兑换码无效，请核对购买记录中的完整兑换码",
		"code_already_redeemed": "兑换码已被其他实例使用，请核对购买记录",
		"code_disabled":         "此批兑换码已停用，请联系发码方",
		"binding_mismatch":      "当前域名或面板实例与授权绑定不一致，请寻求帮助",
		"domain_conflict":       "此域名已绑定其他授权，请寻求帮助",
		"license_revoked":       "此授权已被停用，请寻求帮助",
		"challenge_expired":     "域名验证已过期且兑换尚未完成，请重新填写兑换码",
		"https_not_verified":    "自动验证未通过，请确认本面板域名的 HTTPS 可公开访问，再恢复兑换结果；未核销的码不会被消耗",
		"operation_not_allowed": "当前授权不支持此次兑换；永久正式授权无需续期，兑换码未被消耗",
		"dns_not_verified":      "域名 TXT 证明尚未验证，请核对后重试",
		"http_not_verified":     "域名自动登记尚未验证，请核对公网访问后重试",
		"license_not_found":     "未找到对应授权，请寻求帮助",
	}
	if message, ok := messages[body.Error.Code]; ok && response.StatusCode >= 400 && response.StatusCode < 500 {
		return &UserError{body.Error.Code, message}
	}
	return &UserError{"license_service_unavailable", "授权服务暂未完成请求，请稍后重试；已有兑换可恢复结果"}
}

func (c *Client) ShopCatalog(ctx context.Context) ([]ShopProduct, error) {
	if c.configurationError != nil {
		return nil, c.configurationError
	}
	r := c.request("shop-catalog")
	envelope, err := c.call(ctx, "shop-catalog", "/shop/catalog", r)
	if err != nil {
		return nil, err
	}
	var catalog ShopCatalog
	if err = Verify(envelope, c.config.Keys, &catalog); err != nil {
		return nil, err
	}
	now := c.config.Now()
	if catalog.Purpose != "shop-catalog" || catalog.Version != Version || catalog.Issuer != c.config.IssuerID || catalog.Product != Product || catalog.Audience != Audience || catalog.RequestID != r.RequestID || catalog.IssuedAt.Before(now.Add(-5*time.Minute)) || catalog.IssuedAt.After(now.Add(5*time.Minute)) || !catalog.ExpiresAt.After(now) || !catalog.ExpiresAt.After(catalog.IssuedAt) || catalog.ExpiresAt.Sub(catalog.IssuedAt) > 10*time.Minute {
		return nil, errors.New("invalid signed shop catalog")
	}
	if err = ValidateShopProducts(catalog.Products); err != nil {
		return nil, err
	}
	return catalog.Products, nil
}

func (c *Client) validateRedemption(a RedemptionAttempt, requestID string, fresh bool) error {
	now := c.config.Now()
	if !ValidRedemptionProof(a) || !c.identity(a.Version, a.Issuer, a.Product, a.Audience, a.Domain, a.InstanceID, a.InstanceKeyThumbprint) || a.RequestID == "" || a.IssuedAt.IsZero() || a.IssuedAt.After(now.Add(5*time.Minute)) || !a.ExpiresAt.After(a.IssuedAt) || a.ExpiresAt.Sub(a.IssuedAt) > 10*time.Minute {
		return errors.New("invalid signed redemption receipt")
	}
	// Expired receipts remain useful only for recovering an already committed
	// redemption; the authority decides under its code and license row locks.
	if fresh && (a.RequestID != requestID || a.IssuedAt.Before(now.Add(-5*time.Minute)) || !a.ExpiresAt.After(now)) {
		return errors.New("expired or mismatched redemption receipt")
	}
	return nil
}

func (c *Client) Redeem(ctx context.Context, code string) error {
	if c.configurationError != nil {
		return c.configurationError
	}
	if !c.flight.TryLock() {
		return ErrSyncBusy
	}
	defer c.flight.Unlock()
	c.mu.RLock()
	pending := c.redemption != nil
	c.mu.RUnlock()
	if pending {
		return ErrRedemptionPending
	}
	code = strings.TrimSpace(code)
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(code, "hlr1_"))
	if !strings.HasPrefix(code, "hlr1_") || len(code) != 37 || err != nil || len(raw) != 24 || base64.RawURLEncoding.EncodeToString(raw) != strings.TrimPrefix(code, "hlr1_") {
		return &UserError{"invalid_code", "请填写购买获得的完整兑换码（hlr1_ 开头）"}
	}
	r := c.request("redeem-prepare")
	r.Code = code
	envelope, err := c.call(ctx, "redeem-prepare", "/redemption/prepare", r)
	if err != nil {
		return err
	}
	var attempt RedemptionAttempt
	if err = Verify(envelope, c.config.Keys, &attempt); err != nil {
		return err
	}
	if err = c.validateRedemption(attempt, r.RequestID, true); err != nil {
		return err
	}
	c.mu.Lock()
	previous := c.state
	c.state.Redemption = &envelope
	err = c.saveLocked()
	if err != nil {
		c.state = previous
	} else {
		c.redemption = &attempt
	}
	c.mu.Unlock()
	if err != nil {
		return &UserError{"redemption_save_failed", "无法保存兑换收据，尚未提交激活；请检查面板存储后重试"}
	}
	return c.resumeRedemption(ctx)
}

func (c *Client) ResumeRedemption(ctx context.Context) error {
	if c.configurationError != nil {
		return c.configurationError
	}
	if !c.flight.TryLock() {
		return ErrSyncBusy
	}
	defer c.flight.Unlock()
	return c.resumeRedemption(ctx)
}

func (c *Client) resumeRedemption(ctx context.Context) error {
	c.mu.RLock()
	proof := c.state.Redemption
	c.mu.RUnlock()
	if proof == nil {
		return nil
	}
	r := c.request("redeem-commit")
	r.Challenge = proof
	envelope, err := c.call(ctx, "redeem-commit", "/redemption/commit", r)
	if err == nil {
		if err = c.acceptResult(envelope, r.RequestID, true); err != nil {
			return &UserError{"redemption_result_pending", "兑换结果尚未保存，请恢复兑换结果；不会重复续期"}
		}
		return nil
	}
	var remote *UserError
	if errors.As(err, &remote) {
		switch remote.Code {
		case "challenge_expired", "code_disabled", "code_already_redeemed", "invalid_code", "binding_mismatch", "domain_conflict", "license_revoked", "operation_not_allowed":
			c.mu.Lock()
			previous := c.state
			c.state.Redemption = nil
			if saveErr := c.saveLocked(); saveErr == nil {
				c.redemption = nil
			} else {
				c.state = previous
			}
			c.mu.Unlock()
		}
	}
	return err
}

// ServeProof exposes the same fixed well-known route for two distinct flows.
// Redemption does not require automatic free-registration eligibility.
func (c *Client) ServeProof(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, InstallProofPath+"redeem_") {
		c.ServeInstallProof(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	host := r.Host
	if name, _, err := net.SplitHostPort(host); err == nil {
		host = name
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	proof := c.redemption
	if c.configurationError != nil || r.Method != http.MethodGet || strings.ToLower(host) != c.config.Domain || proof == nil || c.validateRedemption(*proof, "", false) != nil || !proof.ExpiresAt.After(c.config.Now()) || r.URL.Path != proof.HTTPPath || r.URL.RawQuery != "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(proof.HTTPValue))
}
