package panellicense

import (
	"context"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"
)

const InstallFreePurpose = "free-install-registration"
const InstallProofPath = "/.well-known/hl-panel-license/"

// ValidInstallProof accepts only the fixed HTTP-01 contract, never an issuer-
// supplied arbitrary URL, port, file path or a DNS challenge from another flow.
func ValidInstallProof(c Challenge) bool {
	validNonce := func(value string) bool {
		raw, err := base64.RawURLEncoding.DecodeString(value)
		return err == nil && len(raw) == 24 && base64.RawURLEncoding.EncodeToString(raw) == value
	}
	return c.Purpose == InstallFreePurpose && c.VerificationMethod == "http-01" &&
		c.DNSName == "" && c.DNSValue == "" && strings.HasPrefix(c.ReservationID, "free_") &&
		validNonce(strings.TrimPrefix(c.ReservationID, "free_")) &&
		c.HTTPPath == InstallProofPath+c.ReservationID && strings.HasPrefix(c.HTTPValue, "hl-license-v2=") &&
		validNonce(strings.TrimPrefix(c.HTTPValue, "hl-license-v2="))
}

func (c *Client) installEligibleLocked() bool {
	return c.config.AutoRegisterFree && !c.registrationBlocked && c.configurationError == nil &&
		c.claims == nil && c.legacyClaims == nil &&
		(c.challenge == nil || c.challenge.Purpose == InstallFreePurpose)
}

// ReconcileInstallation is safe to retry after network failure, process restart
// or an installer rollback. A previously signed license always takes sync's path.
func (c *Client) ReconcileInstallation(ctx context.Context) error {
	c.mu.RLock()
	eligible := c.installEligibleLocked()
	proof := c.challenge
	c.mu.RUnlock()
	if !eligible {
		return errors.New("本机不符合自动免费登记条件，请保留现有授权身份")
	}
	if proof == nil || !ValidInstallProof(*proof) || !proof.ExpiresAt.After(c.config.Now().Add(time.Minute)) {
		if _, err := c.prepare(ctx, "", "install"); err != nil {
			return err
		}
	}
	return c.commit(ctx, true)
}

func (c *Client) reconcile(ctx context.Context) error {
	c.mu.RLock()
	eligible := c.installEligibleLocked()
	c.mu.RUnlock()
	if !eligible {
		return c.Sync(ctx)
	}
	err := c.ReconcileInstallation(ctx)
	if err != nil {
		c.mu.Lock()
		if c.claims == nil {
			c.lastError = "免费版自动登记尚未完成，请确认管理域名指向本机且公网 80 端口可访问；将自动重试"
		}
		c.mu.Unlock()
	}
	return err
}

// ServeInstallProof exposes only a short-lived random ownership token. The
// instance secret and the signed envelope are never served on this endpoint.
func (c *Client) ServeInstallProof(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	host := r.Host
	if name, _, err := net.SplitHostPort(host); err == nil {
		host = name
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	proof := c.challenge
	if r.Method != http.MethodGet || strings.ToLower(host) != c.config.Domain ||
		!c.installEligibleLocked() || proof == nil || !ValidInstallProof(*proof) ||
		r.URL.Path != proof.HTTPPath || r.URL.RawQuery != "" ||
		!c.identity(proof.Version, proof.Issuer, proof.Product, proof.Audience, proof.Domain, proof.InstanceID, proof.InstanceKeyThumbprint) ||
		proof.IssuedAt.After(c.config.Now().Add(5*time.Minute)) || !proof.ExpiresAt.After(c.config.Now()) ||
		!proof.ExpiresAt.After(proof.IssuedAt) || proof.ExpiresAt.Sub(proof.IssuedAt) > 10*time.Minute {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(proof.HTTPValue))
}
