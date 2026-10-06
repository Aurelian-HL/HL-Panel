package diagnostics

import (
	"fmt"
	"net/netip"
	"strings"
)

type Target struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Kind  string `json:"kind"`
	host  string
}

// ParseTargets accepts an operator-owned, fixed allow-list: label|hostname-or-IP,...
func ParseTargets(raw string) ([]Target, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	if len(parts) > 20 {
		return nil, fmt.Errorf("at most 20 LookingGlass targets are allowed")
	}
	result := make([]Target, 0, len(parts))
	for index, part := range parts {
		fields := strings.Split(part, "|")
		if len(fields) != 2 {
			return nil, fmt.Errorf("LookingGlass target %d must be label|host", index+1)
		}
		label, host := strings.TrimSpace(fields[0]), strings.ToLower(strings.TrimSpace(fields[1]))
		if len(label) == 0 || len(label) > 64 || strings.ContainsAny(label, "\r\n<>\\") {
			return nil, fmt.Errorf("LookingGlass target %d has an invalid label", index+1)
		}
		if err := validateHost(host); err != nil {
			return nil, fmt.Errorf("LookingGlass target %d: %w", index+1, err)
		}
		kind := "hostname"
		if _, err := netip.ParseAddr(host); err == nil {
			kind = "ip"
		}
		result = append(result, Target{ID: fmt.Sprintf("target-%d", index+1), Label: label, Kind: kind, host: host})
	}
	return result, nil
}

func validateHost(host string) error {
	if ip, err := netip.ParseAddr(host); err == nil {
		if !publicIP(ip) {
			return fmt.Errorf("target IP must be public")
		}
		return nil
	}
	if len(host) > 253 || strings.HasSuffix(host, ".") || strings.ContainsAny(host, ":/%@ \\\t\r\n") {
		return fmt.Errorf("target must be a public hostname or IP")
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return fmt.Errorf("hostname must be fully qualified")
	}
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return fmt.Errorf("hostname contains an invalid label")
		}
		for _, char := range label {
			if !(char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '-') {
				return fmt.Errorf("hostname contains an invalid character")
			}
		}
	}
	for _, char := range labels[len(labels)-1] {
		if char < 'a' || char > 'z' {
			return fmt.Errorf("hostname must have an alphabetic suffix")
		}
	}
	return nil
}

var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"), netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/128"), netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("fc00::/7"), netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2001::/32"),
}

func publicIP(ip netip.Addr) bool {
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.Zone() != "" {
		return false
	}
	ip = ip.Unmap()
	for _, prefix := range blockedPrefixes {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}
