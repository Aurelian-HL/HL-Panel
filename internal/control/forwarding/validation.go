package forwarding

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/serviceaddress"
	"net"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

func NormalizeRequest(input Request) (Request, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.CustomerID = strings.TrimSpace(input.CustomerID)
	input.OwnerID = strings.TrimSpace(input.OwnerID)
	input.RuleGroupID = strings.TrimSpace(input.RuleGroupID)
	input.EntryGroupID = strings.TrimSpace(input.EntryGroupID)
	input.ExitGroupID = strings.TrimSpace(input.ExitGroupID)
	input.Description = strings.TrimSpace(input.Description)
	input.VLESSFlow = strings.TrimSpace(input.VLESSFlow)
	input.RealityServerName = strings.TrimSpace(input.RealityServerName)
	input.RealityPublicKey = strings.TrimSpace(input.RealityPublicKey)
	input.RealityShortID = strings.TrimSpace(input.RealityShortID)
	input.RealityDestination = strings.TrimSpace(input.RealityDestination)
	input.VLESSOutboundMode = VLESSOutboundMode(strings.ToUpper(strings.TrimSpace(string(input.VLESSOutboundMode))))
	input.VLESSSOCKS5Host = strings.TrimSpace(input.VLESSSOCKS5Host)
	input.VLESSSOCKS5Username = strings.TrimSpace(input.VLESSSOCKS5Username)
	if input.Name == "" || utf8.RuneCountInString(input.Name) > 128 || strings.IndexFunc(input.Name, unicode.IsControl) >= 0 {
		return Request{}, fmt.Errorf("%w: name must contain 1 to 128 printable characters", faults.ErrValidation)
	}
	if !validReference(input.CustomerID) || !validReference(input.EntryGroupID) || (input.ExitGroupID != "" && !validReference(input.ExitGroupID)) {
		return Request{}, fmt.Errorf("%w: customer_id and entry_group_id must be bounded identifiers", faults.ErrValidation)
	}
	if input.OwnerKind != "" && input.OwnerKind != OwnerCustomer && input.OwnerKind != OwnerAdministrator || input.OwnerID != "" && !validReference(input.OwnerID) {
		return Request{}, fmt.Errorf("%w: invalid rule owner", faults.ErrValidation)
	}
	if input.RuleGroupID != "" && !validReference(input.RuleGroupID) {
		return Request{}, fmt.Errorf("%w: rule_group_id must be empty or a bounded identifier", faults.ErrValidation)
	}
	if input.EgressMode != EgressDirect && input.EgressMode != EgressExitGroup {
		return Request{}, fmt.Errorf("%w: egress_mode must be DIRECT or EXIT_GROUP", faults.ErrValidation)
	}
	if (input.EgressMode == EgressDirect && input.ExitGroupID != "") || (input.EgressMode == EgressExitGroup && input.ExitGroupID == "") {
		return Request{}, fmt.Errorf("%w: exit_group_id is required only for EXIT_GROUP", faults.ErrValidation)
	}
	if input.Protocol != ProtocolTCP && input.Protocol != ProtocolUDP {
		return Request{}, fmt.Errorf("%w: protocol must be tcp or udp", faults.ErrValidation)
	}
	input.IngressProtocol = input.EffectiveIngressProtocol()
	switch input.IngressProtocol {
	case IngressTCP:
		if input.Protocol != ProtocolTCP {
			return Request{}, fmt.Errorf("%w: tcp ingress requires a TCP target protocol", faults.ErrValidation)
		}
	case IngressUDP:
		if input.Protocol != ProtocolUDP {
			return Request{}, fmt.Errorf("%w: udp ingress requires a UDP target protocol", faults.ErrValidation)
		}
	case IngressSOCKS5:
		// NY SOCKS5 is a customer-facing handshake layered over a TCP target.
		// It must never be normalized to the raw NY TCP listener.
		if input.Protocol != ProtocolTCP {
			return Request{}, fmt.Errorf("%w: SOCKS5 ingress requires a TCP target protocol", faults.ErrValidation)
		}
	case IngressVLESSReality:
		if input.Protocol != ProtocolTCP || input.EgressMode != EgressDirect {
			return Request{}, fmt.Errorf("%w: VLESS Reality ingress requires a TCP DIRECT route", faults.ErrValidation)
		}
		if input.VLESSOutboundMode == "" {
			// VLESS rules in the operator workflow always use the single
			// SOCKS5 landing supplied as host:port:username:password.  Keep
			// the default explicit so an omitted legacy field cannot silently
			// bypass the landing with Xray's DIRECT outbound.
			input.VLESSOutboundMode = VLESSOutboundSOCKS5
		}
		if input.VLESSOutboundMode != VLESSOutboundSOCKS5 {
			return Request{}, fmt.Errorf("%w: VLESS Reality ingress requires SOCKS5 outbound", faults.ErrValidation)
		}
		if input.VLESSSOCKS5Host == "" {
			return Request{}, fmt.Errorf("%w: SOCKS5 outbound host is required", faults.ErrValidation)
		}
		host, err := serviceaddress.NormalizeHost(input.VLESSSOCKS5Host)
		if err != nil {
			return Request{}, fmt.Errorf("%w: SOCKS5 outbound host is invalid", faults.ErrValidation)
		}
		input.VLESSSOCKS5Host = host
		if input.VLESSSOCKS5Port < 1 || input.VLESSSOCKS5Port > 65535 {
			return Request{}, fmt.Errorf("%w: SOCKS5 outbound port must be 1 to 65535", faults.ErrValidation)
		}
		if len(input.VLESSSOCKS5Username) > 255 || len(input.VLESSSOCKS5Password) > 255 || strings.IndexFunc(input.VLESSSOCKS5Username, unicode.IsControl) >= 0 || strings.IndexFunc(input.VLESSSOCKS5Password, unicode.IsControl) >= 0 {
			return Request{}, fmt.Errorf("%w: SOCKS5 credentials must be printable and at most 255 characters", faults.ErrValidation)
		}
		// Existing rules may omit write-only credentials while retaining their
		// landing endpoint. Legacy clients may also echo the original username
		// without its password. The repository checks that username under its
		// update lock and rejects changed endpoints or unavailable credentials.
		preserveSOCKS5Credentials := input.Revision > 0 && input.VLESSSOCKS5Password == ""
		if !preserveSOCKS5Credentials && (input.VLESSSOCKS5Username == "" || input.VLESSSOCKS5Password == "") {
			return Request{}, fmt.Errorf("%w: SOCKS5 username and password are both required", faults.ErrValidation)
		}
		if input.AcceptProxyProtocol {
			return Request{}, fmt.Errorf("%w: VLESS Reality ingress does not support receiving Proxy Protocol in this slice", faults.ErrValidation)
		}
		if input.VLESSFlow == "" {
			input.VLESSFlow = "xtls-rprx-vision"
		}
		if input.VLESSFlow != "xtls-rprx-vision" {
			return Request{}, fmt.Errorf("%w: vless_flow must be empty or xtls-rprx-vision", faults.ErrValidation)
		}
		if input.RealityServerName != "" {
			if _, err := serviceaddress.NormalizeHost(input.RealityServerName); err != nil {
				return Request{}, fmt.Errorf("%w: reality_server_name must be one DNS name or IP", faults.ErrValidation)
			}
		}
		if input.RealityPublicKey != "" && !validRealityPublicKey(input.RealityPublicKey) {
			return Request{}, fmt.Errorf("%w: reality_public_key contains invalid characters or length", faults.ErrValidation)
		}
		if input.RealityPublicKey != "" {
			input.RealityPublicKey = canonicalRealityPublicKey(input.RealityPublicKey)
		}
		if input.RealityShortID != "" && !validRealityShortID(input.RealityShortID) {
			return Request{}, fmt.Errorf("%w: reality_short_id must be 2 to 16 hexadecimal characters", faults.ErrValidation)
		}
		if input.RealityDestination == "" && input.RealityServerName != "" {
			// Keep the decoy target explicit in persisted state while retaining
			// compatibility with early clients that only sent SNI.
			input.RealityDestination = net.JoinHostPort(input.RealityServerName, "443")
		}
		if input.RealityDestination != "" {
			host, port, err := net.SplitHostPort(input.RealityDestination)
			if err != nil || strings.TrimSpace(host) == "" {
				return Request{}, fmt.Errorf("%w: reality_destination must be host:port", faults.ErrValidation)
			}
			if _, err := serviceaddress.NormalizeHost(strings.Trim(host, "[]")); err != nil {
				return Request{}, fmt.Errorf("%w: reality_destination host is invalid", faults.ErrValidation)
			}
			parsedPort, err := strconv.Atoi(port)
			if err != nil || parsedPort < 1 || parsedPort > 65535 {
				return Request{}, fmt.Errorf("%w: reality_destination port must be 1 to 65535", faults.ErrValidation)
			}
			input.RealityDestination = net.JoinHostPort(strings.Trim(host, "[]"), strconv.Itoa(parsedPort))
		}
	default:
		return Request{}, fmt.Errorf("%w: ingress_protocol must be tcp, udp, socks5, or vless_reality", faults.ErrValidation)
	}
	if input.IngressProtocol != IngressVLESSReality &&
		(input.VLESSFlow != "" || input.RealityServerName != "" || input.RealityPublicKey != "" || input.RealityShortID != "" || input.RealityDestination != "" || input.VLESSOutboundMode != "" || input.VLESSSOCKS5Host != "" || input.VLESSSOCKS5Port != 0 || input.VLESSSOCKS5Username != "" || input.VLESSSOCKS5Password != "") {
		return Request{}, fmt.Errorf("%w: Reality parameters are only valid for vless_reality ingress", faults.ErrValidation)
	}
	if input.ListenPort < 0 || input.ListenPort > 65535 {
		return Request{}, fmt.Errorf("%w: listen_port must be 0 (automatic) or 1 to 65535", faults.ErrValidation)
	}
	if input.SelectionPolicy != SelectionRoundRobin && input.SelectionPolicy != SelectionRandom && input.SelectionPolicy != SelectionIPHash && input.SelectionPolicy != SelectionLeastLoad && input.SelectionPolicy != SelectionFailover {
		return Request{}, fmt.Errorf("%w: invalid selection_policy", faults.ErrValidation)
	}
	if input.SendProxyProtocol < SendProxyDisabled || input.SendProxyProtocol > SendProxyV2TCP {
		return Request{}, fmt.Errorf("%w: send_proxy_protocol must be 0, 1, 2 or 3", faults.ErrValidation)
	}
	if input.TrafficLimitBytes < 0 || input.TrafficLimitBytes > 9_007_199_254_740_991 {
		return Request{}, fmt.Errorf("%w: 规则流量额度必须为 0 或安全范围内的正整数", faults.ErrValidation)
	}
	if input.SpeedLimitMbps < 0 || input.SpeedLimitMbps > 1_000_000 {
		return Request{}, fmt.Errorf("%w: speed_limit_mbps must be between 0 and 1000000", faults.ErrValidation)
	}
	if input.IPLimit < 0 || input.IPLimit > 1_000_000 || input.ConnectionLimit < 0 || input.ConnectionLimit > 1_000_000 {
		return Request{}, fmt.Errorf("%w: ip_limit and connection_limit must be between 0 and 1000000", faults.ErrValidation)
	}
	if input.Protocol == ProtocolUDP && (input.AcceptProxyProtocol || input.SpeedLimitMbps != 0 || input.IPLimit != 0 || input.ConnectionLimit != 0 || (input.SendProxyProtocol != SendProxyDisabled && input.SendProxyProtocol != SendProxyV2TCPUDP)) {
		return Request{}, fmt.Errorf("%w: UDP rules only allow disabled or v2 TCP+UDP send Proxy Protocol and do not support receive Proxy Protocol, speed, IP or connection limits", faults.ErrValidation)
	}
	// A VLESS SOCKS5 outbound receives the final destination from the
	// authenticated VLESS request. It therefore does not need a persisted
	// fixed target. DIRECT VLESS and every other ingress retain the normal
	// target requirements.
	minimumTargets := 1
	dynamicVLESSSOCKS5 := input.EffectiveIngressProtocol() == IngressVLESSReality && input.VLESSOutboundMode == VLESSOutboundSOCKS5
	if dynamicVLESSSOCKS5 {
		minimumTargets = 0
	}
	if len(input.Targets) < minimumTargets || len(input.Targets) > 32 {
		if minimumTargets == 0 {
			return Request{}, fmt.Errorf("%w: provide 0 to 32 targets for dynamic VLESS SOCKS5 egress", faults.ErrValidation)
		}
		return Request{}, fmt.Errorf("%w: provide 1 to 32 targets", faults.ErrValidation)
	}
	if input.EffectiveIngressProtocol() == IngressVLESSReality && !dynamicVLESSSOCKS5 && len(input.Targets) != 1 {
		return Request{}, fmt.Errorf("%w: VLESS Reality currently requires exactly one target", faults.ErrValidation)
	}
	if dynamicVLESSSOCKS5 && len(input.Targets) > 1 {
		return Request{}, fmt.Errorf("%w: dynamic VLESS SOCKS5 egress accepts at most one compatibility target", faults.ErrValidation)
	}
	targets := make([]Target, len(input.Targets))
	seen := make(map[Target]struct{}, len(input.Targets))
	for index, target := range input.Targets {
		host, err := serviceaddress.NormalizeHost(target.Host)
		if err != nil {
			return Request{}, fmt.Errorf("%w: targets[%d].host must be a DNS name or IP without credentials, protocol, path or port", faults.ErrValidation, index)
		}
		if target.Port < 1 || target.Port > 65535 {
			return Request{}, fmt.Errorf("%w: targets[%d].port must be 1 to 65535", faults.ErrValidation, index)
		}
		targets[index] = Target{Host: host, Port: target.Port}
		if _, ok := seen[targets[index]]; ok {
			return Request{}, fmt.Errorf("%w: duplicate target", faults.ErrValidation)
		}
		seen[targets[index]] = struct{}{}
	}
	input.Targets = targets
	if utf8.RuneCountInString(input.Description) > 1024 {
		return Request{}, fmt.Errorf("%w: description exceeds 1024 characters", faults.ErrValidation)
	}
	if input.Revision < 0 {
		return Request{}, fmt.Errorf("%w: revision must not be negative", faults.ErrValidation)
	}
	return input, nil
}

func validRealityPublicKey(value string) bool {
	// Xray Reality public keys are X25519 points encoded as unpadded
	// base64url (43 characters for 32 bytes). Accept one trailing padding
	// character for imports produced by standard base64url encoders.
	if len(value) != 43 && len(value) != 44 {
		return false
	}
	if len(value) == 44 && value[43] != '=' {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimSuffix(value, "="))
	return err == nil && len(decoded) == 32
}

func canonicalRealityPublicKey(value string) string {
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimSuffix(strings.TrimSpace(value), "="))
	if err != nil || len(decoded) != 32 {
		return strings.TrimSpace(value)
	}
	return base64.RawURLEncoding.EncodeToString(decoded)
}

func validRealityShortID(value string) bool {
	if len(value) < 2 || len(value) > 16 || len(value)%2 != 0 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validReference(value string) bool {
	return value != "" && len(value) <= 128 && strings.IndexFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) < 0
}
