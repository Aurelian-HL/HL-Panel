// Package vlessconfig compiles the bounded VLESS Reality + Vision ingress
// subset used by direct TCP rules. It does not create identities or persist
// TLS material; callers must obtain those values from an authorized secret
// store before asking for a runtime configuration.
package vlessconfig

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/serviceaddress"
)

var (
	ErrUnsupportedRoute  = errors.New("VLESS Reality compiler supports TCP DIRECT or SOCKS5 egress")
	ErrRuntimeMaterial   = errors.New("VLESS Reality runtime material is incomplete")
	ErrInvalidRealityKey = errors.New("VLESS Reality private key is invalid")
)

type Listener struct {
	Address string
	Port    int
}

type RealityProfile struct {
	ServerName string
	Dest       string
	PrivateKey string
	ShortIDs   []string
	Flow       string
}

type Identity struct {
	UUID string
}

// ProbeIdentity is a separate, non-customer VLESS client. Its traffic is
// redirected to a dedicated echo process bound on the same node.
type ProbeIdentity struct {
	UUID     string
	EchoPort int
}

// SOCKS5Upstream is confidential runtime material. The forwarding repository
// owns persistence; this compiler only receives it while producing a node
// bundle and never exposes it through a rule projection.
type SOCKS5Upstream struct {
	Hostname string
	Port     int
	Username string
	Password string
}

var shortIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{2,16}$`)

// CompileDirect compiles one VLESS Reality + Vision ingress. A single fixed
// target is required because Xray freedom.redirect has one destination; a
// multi-target rule must wait for the connection-aware scheduler adapter.
func CompileDirect(rule forwarding.Rule, identity Identity, profile RealityProfile, listener Listener) ([]byte, error) {
	return compileDirect(rule, identity, profile, listener, nil, nil)
}

// CompileDirectWithSOCKS5 compiles the same VLESS Reality/Vision ingress with
// an authenticated SOCKS5 landing proxy. The customer-facing protocol and URI
// remain VLESS; the destination requested by the VLESS client is passed to the
// SOCKS5 server. The landing address is never used as the final target.
func CompileDirectWithSOCKS5(rule forwarding.Rule, identity Identity, profile RealityProfile, listener Listener, upstream SOCKS5Upstream) ([]byte, error) {
	return compileDirect(rule, identity, profile, listener, &upstream, nil)
}

// CompileDirectWithProbe adds an isolated identity and a fixed loopback echo
// route. A probe cannot select an arbitrary outbound destination.
func CompileDirectWithProbe(rule forwarding.Rule, identity Identity, profile RealityProfile, listener Listener, probe ProbeIdentity) ([]byte, error) {
	if err := validateIdentity(Identity{UUID: probe.UUID}); err != nil || strings.EqualFold(probe.UUID, identity.UUID) || probe.EchoPort < 1 || probe.EchoPort > 65535 {
		return nil, ErrRuntimeMaterial
	}
	return compileDirect(rule, identity, profile, listener, nil, &probe)
}

// CompileDirectWithProbeAndSOCKS5 combines the isolated probe identity with a
// SOCKS5 landing route. Probe traffic remains on its loopback echo outbound.
func CompileDirectWithProbeAndSOCKS5(rule forwarding.Rule, identity Identity, profile RealityProfile, listener Listener, probe ProbeIdentity, upstream SOCKS5Upstream) ([]byte, error) {
	if err := validateIdentity(Identity{UUID: probe.UUID}); err != nil || strings.EqualFold(probe.UUID, identity.UUID) || probe.EchoPort < 1 || probe.EchoPort > 65535 {
		return nil, ErrRuntimeMaterial
	}
	return compileDirect(rule, identity, profile, listener, &upstream, &probe)
}

func compileDirect(rule forwarding.Rule, identity Identity, profile RealityProfile, listener Listener, socks5 *SOCKS5Upstream, probe *ProbeIdentity) ([]byte, error) {
	if rule.EffectiveIngressProtocol() != forwarding.IngressVLESSReality || rule.Protocol != forwarding.ProtocolTCP || rule.EgressMode != forwarding.EgressDirect {
		return nil, ErrUnsupportedRoute
	}
	if socks5 == nil && rule.VLESSOutboundMode == forwarding.VLESSOutboundSOCKS5 {
		return nil, ErrUnsupportedRoute
	}
	if socks5 != nil && rule.VLESSOutboundMode != forwarding.VLESSOutboundSOCKS5 {
		return nil, ErrUnsupportedRoute
	}
	// DIRECT uses one explicit target because freedom.redirect is a fixed route.
	// SOCKS5 keeps the destination from the authenticated VLESS request, so its
	// rule targets are metadata only and must not constrain the runtime route.
	if socks5 == nil && len(rule.Targets) != 1 {
		return nil, fmt.Errorf("%w: exactly one target is required for the direct Xray adapter", ErrUnsupportedRoute)
	}
	if err := validateIdentity(identity); err != nil {
		return nil, err
	}
	if err := validateProfile(profile); err != nil {
		return nil, err
	}
	if err := validateListener(listener); err != nil {
		return nil, err
	}
	profile.Flow = strings.TrimSpace(profile.Flow)
	if profile.Flow == "" {
		profile.Flow = "xtls-rprx-vision"
	}
	if profile.Flow != "xtls-rprx-vision" {
		return nil, errors.New("VLESS Reality flow must be xtls-rprx-vision")
	}
	client := map[string]any{"id": identity.UUID, "flow": profile.Flow}
	inboundTag := statsTag("vless-reality-", rule)
	clients := []any{client}
	outboundTag := statsTag("direct-", rule)
	var outbounds []any
	if socks5 != nil {
		if err := validateSOCKS5Upstream(*socks5); err != nil {
			return nil, err
		}
		socksTag := statsTag("socks-landing-", rule)
		outboundTag = socksTag
		server := map[string]any{"address": socks5.Hostname, "port": socks5.Port}
		if socks5.Username != "" {
			server["users"] = []any{map[string]any{"user": socks5.Username, "pass": socks5.Password}}
		}
		// With no freedom.redirect, Xray's SOCKS outbound receives the
		// destination carried by the VLESS request and issues CONNECT there.
		outbounds = append(outbounds, map[string]any{
			"tag": socksTag, "protocol": "socks",
			"settings": map[string]any{"servers": []any{server}},
		})
	} else {
		target := rule.Targets[0]
		host, err := serviceaddress.NormalizeHost(target.Host)
		if err != nil || target.Port < 1 || target.Port > 65535 {
			return nil, errors.New("VLESS Reality target is invalid")
		}
		outbounds = append(outbounds, map[string]any{
			"tag":      outboundTag,
			"protocol": "freedom",
			"settings": map[string]any{"redirect": net.JoinHostPort(host, fmt.Sprint(target.Port))},
		})
	}
	routes := []any{}
	if probe != nil {
		probeEmail := "hl-probe-" + rule.ID
		probeTag := "probe-echo-" + rule.ID
		clients = append(clients, map[string]any{"id": probe.UUID, "flow": profile.Flow, "email": probeEmail})
		outbounds = append(outbounds, map[string]any{
			"tag": probeTag, "protocol": "freedom",
			"settings": map[string]any{"redirect": net.JoinHostPort("127.0.0.1", fmt.Sprint(probe.EchoPort))},
		})
		routes = append(routes, map[string]any{
			"type": "field", "inboundTag": []string{inboundTag}, "user": []string{probeEmail}, "outboundTag": probeTag,
		})
	}
	routes = append(routes, map[string]any{"type": "field", "inboundTag": []string{inboundTag}, "outboundTag": outboundTag})
	config := map[string]any{
		"log": map[string]any{"loglevel": "warning", "access": "none"},
		"api": map[string]any{"tag": "hl-stats-api", "services": []string{"StatsService"}},
		"stats": map[string]any{},
		"inbounds": []any{map[string]any{
			"tag":      inboundTag,
			"listen":   listener.Address,
			"port":     listener.Port,
			"protocol": "vless",
			"settings": map[string]any{"clients": clients, "decryption": "none"},
			"streamSettings": map[string]any{
				"network":  "tcp",
				"security": "reality",
				"realitySettings": map[string]any{
					"show":        false,
					"dest":        profile.Dest,
					"xver":        0,
					"serverNames": []string{profile.ServerName},
					"privateKey":  profile.PrivateKey,
					"shortIds":    profile.ShortIDs,
				},
			},
		}},
		"outbounds": outbounds,
		"routing":   map[string]any{"rules": routes},
	}
	return json.Marshal(config)
}

// statsTag keeps the human-readable rule prefix while carrying the exact
// business identity required by the edge usage reporter. Xray exposes tags
// verbatim through StatsService; URL-safe base64 avoids delimiters and
// control characters in customer/group IDs.
func statsTag(prefix string, rule forwarding.Rule) string {
	metadata := struct {
		RuleID      string `json:"rule_id"`
		CustomerID  string `json:"customer_id"`
		EntryGroup  string `json:"entry_group_id"`
		ExitGroup   string `json:"exit_group_id"`
		Protocol    string `json:"protocol"`
	}{
		RuleID: rule.ID, CustomerID: rule.CustomerID, EntryGroup: rule.EntryGroupID,
		ExitGroup: rule.ExitGroupID, Protocol: string(rule.Protocol),
	}
	raw, _ := json.Marshal(metadata)
	return prefix + rule.ID + "--" + base64.RawURLEncoding.EncodeToString(raw)
}

func validateSOCKS5Upstream(upstream SOCKS5Upstream) error {
	host, err := serviceaddress.NormalizeHost(strings.TrimSpace(upstream.Hostname))
	if err != nil || host == "" || upstream.Port < 1 || upstream.Port > 65535 {
		return ErrRuntimeMaterial
	}
	if (upstream.Username == "") != (upstream.Password == "") || utf8.RuneCountInString(upstream.Username) > 255 || utf8.RuneCountInString(upstream.Password) > 255 || strings.IndexFunc(upstream.Username, unicode.IsControl) >= 0 || strings.IndexFunc(upstream.Password, unicode.IsControl) >= 0 {
		return ErrRuntimeMaterial
	}
	return nil
}

func validateIdentity(identity Identity) error {
	uuid := strings.TrimSpace(identity.UUID)
	if len(uuid) != 36 || uuid[8] != '-' || uuid[13] != '-' || uuid[18] != '-' || uuid[23] != '-' {
		return ErrRuntimeMaterial
	}
	if _, err := hex.DecodeString(strings.ReplaceAll(uuid, "-", "")); err != nil {
		return ErrRuntimeMaterial
	}
	return nil
}

func validateProfile(profile RealityProfile) error {
	profile.ServerName = strings.TrimSpace(profile.ServerName)
	if _, err := serviceaddress.NormalizeHost(profile.ServerName); err != nil || profile.ServerName == "" {
		return ErrRuntimeMaterial
	}
	profile.Dest = strings.TrimSpace(profile.Dest)
	host, port, err := net.SplitHostPort(profile.Dest)
	if err != nil || host == "" || port == "" {
		return ErrRuntimeMaterial
	}
	if _, err := serviceaddress.NormalizeHost(host); err != nil {
		return ErrRuntimeMaterial
	}
	if profile.PrivateKey == "" || !validX25519PrivateKey(profile.PrivateKey) {
		return ErrInvalidRealityKey
	}
	if len(profile.ShortIDs) == 0 || len(profile.ShortIDs) > 16 {
		return ErrRuntimeMaterial
	}
	for _, shortID := range profile.ShortIDs {
		if !shortIDPattern.MatchString(strings.TrimSpace(shortID)) {
			return ErrRuntimeMaterial
		}
	}
	return nil
}

func validX25519PrivateKey(value string) bool {
	value = strings.TrimSpace(value)
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(decoded) == 32
}

func validateListener(listener Listener) error {
	address := strings.TrimSpace(listener.Address)
	if address == "" {
		return ErrRuntimeMaterial
	}
	if parsed, err := netip.ParseAddr(address); err != nil || parsed.Zone() != "" || listener.Port < 1 || listener.Port > 65535 {
		return ErrRuntimeMaterial
	}
	return nil
}
