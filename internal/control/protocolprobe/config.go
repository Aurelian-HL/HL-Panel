// Package protocolprobe checks a VLESS Reality Vision path against a dedicated
// echo target. It does not publish gateway health or accept arbitrary targets.
package protocolprobe

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/hongle/hl-panel/internal/serviceaddress"
)

var ErrInvalidSpec = errors.New("invalid VLESS protocol probe specification")

const DefaultEchoPort = 19090

var shortIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{2,16}$`)

// Spec contains public endpoint material and a probe-only VLESS identity.
// EchoHost:EchoPort must be the dedicated target of the server-side rule.
type Spec struct {
	XrayBinary string
	DialHost   string
	DialPort   int
	UUID       string
	ServerName string
	PublicKey  string
	ShortID    string
	EchoHost   string
	EchoPort   int
}

func (s Spec) validate() error {
	if _, err := serviceaddress.NormalizeHost(s.DialHost); err != nil {
		return ErrInvalidSpec
	}
	if _, err := serviceaddress.NormalizeHost(s.ServerName); err != nil {
		return ErrInvalidSpec
	}
	if _, err := serviceaddress.NormalizeHost(s.EchoHost); err != nil {
		return ErrInvalidSpec
	}
	if s.DialPort < 1 || s.DialPort > 65535 || s.EchoPort < 1 || s.EchoPort > 65535 || len(s.EchoHost) > 255 {
		return ErrInvalidSpec
	}
	uuid := strings.TrimSpace(s.UUID)
	if len(uuid) != 36 || uuid[8] != '-' || uuid[13] != '-' || uuid[18] != '-' || uuid[23] != '-' {
		return ErrInvalidSpec
	}
	if _, err := hex.DecodeString(strings.ReplaceAll(uuid, "-", "")); err != nil {
		return ErrInvalidSpec
	}
	key, err := base64.RawURLEncoding.DecodeString(s.PublicKey)
	if err != nil || len(key) != 32 || !shortIDPattern.MatchString(s.ShortID) {
		return ErrInvalidSpec
	}
	return nil
}

func clientConfig(s Spec, socksPort int) ([]byte, error) {
	if err := s.validate(); err != nil || socksPort < 1 || socksPort > 65535 {
		return nil, ErrInvalidSpec
	}
	config := map[string]any{
		"log": map[string]any{"loglevel": "none", "access": "none"},
		"inbounds": []any{map[string]any{
			"tag": "probe-socks", "listen": "127.0.0.1", "port": socksPort,
			"protocol": "socks", "settings": map[string]any{"auth": "noauth", "udp": false},
		}},
		"outbounds": []any{map[string]any{
			"tag": "probe-vless", "protocol": "vless",
			"settings": map[string]any{"vnext": []any{map[string]any{
				"address": s.DialHost, "port": s.DialPort,
				"users": []any{map[string]any{"id": s.UUID, "encryption": "none", "flow": "xtls-rprx-vision"}},
			}}},
			"streamSettings": map[string]any{"network": "tcp", "security": "reality",
				"realitySettings": map[string]any{
					"fingerprint": "chrome", "serverName": s.ServerName,
					"publicKey": s.PublicKey, "shortId": s.ShortID, "spiderX": "/",
				}},
		}},
	}
	return json.Marshal(config)
}
