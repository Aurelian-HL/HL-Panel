package vless

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/netip"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/hongle/hl-panel/internal/serviceaddress"
)

func (p Profile) Validate() error {
	if !validHost(p.Endpoint.Hostname) || p.Endpoint.Port < 1 || p.Endpoint.Port > 65535 {
		return errors.New("VLESS endpoint requires one hostname and a valid port")
	}
	if len(p.Endpoint.Name) > 128 || strings.ContainsAny(p.Endpoint.Name, "\r\n\x00") {
		return errors.New("VLESS endpoint name is invalid")
	}
	uuid := p.Identity.UUID
	if len(uuid) != 36 || uuid[8] != '-' || uuid[13] != '-' || uuid[18] != '-' || uuid[23] != '-' {
		return errors.New("VLESS identity requires a canonical UUID")
	}
	raw, err := hex.DecodeString(strings.ReplaceAll(uuid, "-", ""))
	if err != nil || len(raw) != 16 {
		return errors.New("VLESS identity requires a canonical UUID")
	}
	if p.Identity.Flow != "" && p.Identity.Flow != "xtls-rprx-vision" {
		return errors.New("VLESS flow is unsupported")
	}
	if p.Reality != nil {
		if p.Identity.Flow != "xtls-rprx-vision" {
			return errors.New("Reality requires xtls-rprx-vision flow")
		}
		return p.Reality.validate()
	}
	if !validHost(p.TLS.ServerName) {
		return errors.New("VLESS TLS server name is required")
	}
	return nil
}

func (p *RealityProfile) validate() error {
	if p == nil || !validHost(p.ServerName) {
		return errors.New("Reality server name is required")
	}
	if p.PublicKey != "" && !validRealityKey(p.PublicKey) {
		return errors.New("Reality public key is invalid")
	}
	if p.PrivateKey != "" && !validRealityKey(p.PrivateKey) {
		return errors.New("Reality private key is invalid")
	}
	if p.PublicKey != "" && p.PrivateKey != "" {
		private, _ := base64.RawURLEncoding.DecodeString(p.PrivateKey)
		public, _ := base64.RawURLEncoding.DecodeString(p.PublicKey)
		if subtle.ConstantTimeCompare(x25519PublicKey(private), public) != 1 {
			return errors.New("Reality public and private keys do not match")
		}
	}
	if p.ShortID == "" || len(p.ShortID) < 2 || len(p.ShortID) > 16 || len(p.ShortID)%2 != 0 {
		return errors.New("Reality short ID must contain 2 to 16 hexadecimal characters")
	}
	if _, err := hex.DecodeString(p.ShortID); err != nil {
		return errors.New("Reality short ID must contain hexadecimal characters")
	}
	if p.Destination == "" {
		return errors.New("Reality destination is required")
	}
	destinationHost, destinationPort, err := net.SplitHostPort(p.Destination)
	if err != nil || !validHost(strings.Trim(destinationHost, "[]")) || destinationPort == "" {
		return errors.New("Reality destination must be a host and port")
	}
	port, err := strconv.Atoi(destinationPort)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("Reality destination port must be 1 to 65535")
	}
	if p.Fingerprint == "" {
		p.Fingerprint = "chrome"
	}
	switch p.Fingerprint {
	case "chrome", "firefox", "safari", "edge", "ios", "android", "random", "randomized":
	default:
		return errors.New("Reality fingerprint is unsupported")
	}
	if p.SpiderX != "" && (!strings.HasPrefix(p.SpiderX, "/") || strings.ContainsAny(p.SpiderX, "\r\n\x00")) {
		return errors.New("Reality spiderX must be an absolute URL path")
	}
	return nil
}

func validRealityKey(value string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(raw) == 32
}

func generateRealityKeyPair(random io.Reader) (publicKey, privateKey string, err error) {
	if random == nil {
		return "", "", errors.New("random source is required")
	}
	private, err := generateX25519PrivateKey(random)
	if err != nil {
		return "", "", err
	}
	public := x25519PublicKey(private)
	return base64.RawURLEncoding.EncodeToString(public), base64.RawURLEncoding.EncodeToString(private), nil
}

func (l Listener) validate() error {
	if _, err := netip.ParseAddr(l.Address); err != nil || l.Port < 1 || l.Port > 65535 {
		return errors.New("VLESS listener requires an IP address and a valid port")
	}
	for _, file := range []string{l.CertificateFile, l.PrivateKeyFile} {
		if (!filepath.IsAbs(file) && !path.IsAbs(file)) || strings.ContainsAny(file, "\r\n\x00") {
			return errors.New("VLESS TLS certificate and key require absolute file paths")
		}
	}
	return nil
}

func (l Listener) validateReality() error {
	if _, err := netip.ParseAddr(l.Address); err != nil || l.Port < 1 || l.Port > 65535 {
		return errors.New("VLESS Reality listener requires an IP address and a valid port")
	}
	return nil
}

func validHost(host string) bool {
	_, err := serviceaddress.NormalizeHost(host)
	return err == nil && strings.TrimSpace(host) == host
}
