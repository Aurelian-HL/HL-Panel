// Package testfixture provides synthetic credentials for isolated tests only.
package testfixture

import (
	"encoding/base64"
	"encoding/json"
	"strings"
)

func Document() map[string]any {
	settings := map[string]any{"clients": []any{map[string]any{"id": "11111111-1111-4111-8111-111111111111", "flow": ""}}, "decryption": "none"}
	stream := map[string]any{"network": "tcp", "security": "reality", "realitySettings": map[string]any{
		"dest": "example.org:443", "privateKey": base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("p", 32))),
		"serverNames": []string{"example.org", "www.example.org"}, "shortIds": []string{"01", "02"},
		"mldsa65Seed": base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("m", 32))),
	}}
	source := map[string]any{"id": 1, "port": 25510, "protocol": "vless", "settings": settings, "streamSettings": stream, "enable": true, "up": json.Number("9007199254740993")}
	inbound := map[string]any{"tag": "inbound-25510", "port": 25510, "protocol": "vless", "settings": settings, "streamSettings": stream}
	return map[string]any{"schema": "hl-panel.imported-config/v1", "source_id": "kl-one", "host": "source.example.invalid", "observed_at": "2026-10-15T00:00:00Z",
		"inbounds":      []any{map[string]any{"id": 1, "port": 25510, "name": "隔离测试", "owner_id": "customer-one", "raw": source}},
		"config":        map[string]any{"inbounds": []any{inbound}, "outbounds": []any{map[string]any{"tag": "direct", "protocol": "freedom", "settings": map[string]any{}}}, "routing": map[string]any{"rules": []any{}}},
		"subscriptions": []any{map[string]any{"subscription_token": "SYNTHETIC-SECRET-TOKEN"}},
	}
}

func Raw() []byte { b, _ := json.Marshal(Document()); return b }
