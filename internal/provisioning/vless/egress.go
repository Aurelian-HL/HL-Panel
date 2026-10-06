package vless

import "errors"

type EgressMode string

const (
	EgressDirect      EgressMode = "DIRECT"
	EgressSOCKSSingle EgressMode = "SOCKS_SINGLE"
)

// Egress is independent from customer-facing VLESS protocol and identity.
// A SOCKS5 landing proxy changes outbound routing, not the customer's URI.
type Egress struct {
	Mode   EgressMode
	SOCKS5 *SOCKS5Upstream
}

type SOCKS5Upstream struct {
	Hostname string
	Port     int
	Username string `json:"-"`
	Password string `json:"-"`
}

func (e Egress) nativeOutbound() (map[string]any, error) {
	switch e.Mode {
	case EgressDirect:
		if e.SOCKS5 != nil {
			return nil, errors.New("DIRECT egress cannot also specify a SOCKS5 landing proxy")
		}
		return map[string]any{"tag": "direct", "protocol": "freedom"}, nil
	case EgressSOCKSSingle:
		upstream := e.SOCKS5
		if upstream == nil || !validHost(upstream.Hostname) || upstream.Port < 1 || upstream.Port > 65535 {
			return nil, errors.New("SOCKS_SINGLE requires one valid landing proxy")
		}
		if (upstream.Username == "") != (upstream.Password == "") || len(upstream.Username) > 255 || len(upstream.Password) > 255 {
			return nil, errors.New("SOCKS5 credentials must both be present and each fit in 255 bytes, or both be absent")
		}
		server := map[string]any{"address": upstream.Hostname, "port": upstream.Port}
		if upstream.Username != "" {
			server["users"] = []any{map[string]any{"user": upstream.Username, "pass": upstream.Password}}
		}
		return map[string]any{"tag": "socks-landing", "protocol": "socks", "settings": map[string]any{"servers": []any{server}}}, nil
	default:
		return nil, errors.New("unsupported egress mode; DIRECT or SOCKS_SINGLE must be chosen explicitly")
	}
}
