package licensing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// RemoteIssuer is the client used by a licensed target panel. The issuer URL
// is kept separate from the local panel API so a target never needs a copy of
// the issuer's license database.
type RemoteIssuer struct {
	baseURL string
	client  *http.Client
}

func NewRemoteIssuer(rawURL string, timeout time.Duration) (*RemoteIssuer, error) {
	value := strings.TrimRight(strings.TrimSpace(rawURL), "/")
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("license issuer URL must be an absolute URL without query or fragment")
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return nil, errors.New("license issuer URL must use http or https")
	}
	if parsed.User != nil {
		return nil, errors.New("license issuer URL must not contain userinfo")
	}
	if parsed.Scheme == "http" && !isLoopbackHost(parsed.Hostname()) {
		return nil, errors.New("license issuer HTTP is only allowed on loopback; use HTTPS for remote issuers")
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &RemoteIssuer{baseURL: value, client: &http.Client{Timeout: timeout}}, nil
}

func isLoopbackHost(host string) bool {
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (c *RemoteIssuer) Activate(ctx context.Context, code, domain, instanceID string) (Lease, error) {
	var lease Lease
	err := c.post(ctx, "/api/v1/public/license/activate", map[string]string{
		"code": code, "domain": domain, "instance_id": instanceID,
	}, &lease)
	return lease, err
}

func (c *RemoteIssuer) Renew(ctx context.Context, licenseID, code, domain, instanceID string) (Lease, error) {
	var lease Lease
	err := c.post(ctx, "/api/v1/public/license/renew", map[string]string{
		"license_id": licenseID, "code": code, "domain": domain, "instance_id": instanceID,
	}, &lease)
	return lease, err
}

func (c *RemoteIssuer) post(ctx context.Context, path string, payload any, output any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode license request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create license request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	response, err := c.client.Do(request)
	if err != nil {
		return fmt.Errorf("license issuer request failed: %w", err)
	}
	defer response.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if readErr != nil {
		return fmt.Errorf("read license issuer response: %w", readErr)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var envelope struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &envelope)
		switch envelope.Error.Code {
		case "license_expired":
			return ErrExpired
		case "license_revoked":
			return ErrRevoked
		case "license_bound":
			return ErrBound
		case "license_domain_mismatch":
			return ErrDomainMismatch
		default:
			return fmt.Errorf("license issuer returned HTTP %d", response.StatusCode)
		}
	}
	if err := json.Unmarshal(raw, output); err != nil {
		return fmt.Errorf("decode license issuer response: %w", err)
	}
	return nil
}
