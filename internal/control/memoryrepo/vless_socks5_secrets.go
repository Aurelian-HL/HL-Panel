package memoryrepo

import (
	"fmt"
	"net"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/hongle/hl-panel/internal/control/faults"
	provisioningvless "github.com/hongle/hl-panel/internal/provisioning/vless"
	"github.com/hongle/hl-panel/internal/serviceaddress"
)

// normalizeVLESSSOCKS5Upstream validates and canonicalizes the confidential
// SOCKS5 landing endpoint. It intentionally returns a provisioning type whose
// credential fields have json:"-" tags; callers must keep it out of public
// rule/list/transfer models.
func normalizeVLESSSOCKS5Upstream(ruleID string, input provisioningvless.SOCKS5Upstream) (provisioningvless.SOCKS5Upstream, error) {
	if !validVLESSSOCKS5Reference(strings.TrimSpace(ruleID)) {
		return provisioningvless.SOCKS5Upstream{}, fmt.Errorf("%w: VLESS SOCKS5 rule ID is invalid", faults.ErrValidation)
	}
	host, err := serviceaddress.NormalizeHost(strings.TrimSpace(input.Hostname))
	if err != nil || host == "" {
		return provisioningvless.SOCKS5Upstream{}, fmt.Errorf("%w: VLESS SOCKS5 host is invalid", faults.ErrValidation)
	}
	if input.Port < 1 || input.Port > 65535 {
		return provisioningvless.SOCKS5Upstream{}, fmt.Errorf("%w: VLESS SOCKS5 port must be 1 to 65535", faults.ErrValidation)
	}
	if (input.Username == "") != (input.Password == "") {
		return provisioningvless.SOCKS5Upstream{}, fmt.Errorf("%w: VLESS SOCKS5 username and password must be provided together", faults.ErrValidation)
	}
	if utf8.RuneCountInString(input.Username) > 255 || utf8.RuneCountInString(input.Password) > 255 ||
		strings.IndexFunc(input.Username, unicode.IsControl) >= 0 || strings.IndexFunc(input.Password, unicode.IsControl) >= 0 {
		return provisioningvless.SOCKS5Upstream{}, fmt.Errorf("%w: VLESS SOCKS5 credentials contain invalid characters or exceed 255 characters", faults.ErrValidation)
	}
	// NormalizeHost can accept an IPv6 literal without brackets. It is safe to
	// retain the canonical host text here because the Xray JSON compiler writes
	// it as an address field, not as host:port text.
	if net.ParseIP(host) == nil {
		host = strings.ToLower(host)
	}
	return provisioningvless.SOCKS5Upstream{Hostname: host, Port: input.Port, Username: input.Username, Password: input.Password}, nil
}

func validVLESSSOCKS5Reference(value string) bool {
	return value != "" && len(value) <= 128 && strings.IndexFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}) < 0
}

// setVLESSSOCKS5UpstreamLocked is used by forwarding repository transactions.
// The caller must hold Store.mu and perform rule/idempotency rollback together
// with the rule mutation.
func (s *Store) setVLESSSOCKS5UpstreamLocked(ruleID string, input provisioningvless.SOCKS5Upstream) error {
	normalized, err := normalizeVLESSSOCKS5Upstream(ruleID, input)
	if err != nil {
		return err
	}
	if _, exists := s.forwardRules[ruleID]; !exists {
		return fmt.Errorf("%w: forwarding rule does not exist", faults.ErrNotFound)
	}
	if s.vlessSOCKS5Upstreams == nil {
		s.vlessSOCKS5Upstreams = make(map[string]provisioningvless.SOCKS5Upstream)
	}
	s.vlessSOCKS5Upstreams[ruleID] = normalized
	return nil
}

func (s *Store) deleteVLESSSOCKS5UpstreamLocked(ruleID string) {
	delete(s.vlessSOCKS5Upstreams, ruleID)
}

// vlessSOCKS5UpstreamLocked returns a copy, so callers cannot mutate the
// confidential map without going through validation.
func (s *Store) vlessSOCKS5UpstreamLocked(ruleID string) (provisioningvless.SOCKS5Upstream, bool) {
	upstream, ok := s.vlessSOCKS5Upstreams[ruleID]
	return upstream, ok
}

// VLESSSOCKS5Upstream is an internal control-plane accessor for bundle
// compilation. It is intentionally not wired to an HTTP response.
func (s *Store) VLESSSOCKS5Upstream(ruleID string) (provisioningvless.SOCKS5Upstream, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.vlessSOCKS5UpstreamLocked(strings.TrimSpace(ruleID))
}
