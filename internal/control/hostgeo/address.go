// Package hostgeo resolves display-only host geography, separate from routing.
package hostgeo

import (
	"net/netip"
	"strings"
)

var nonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001:db8::/32"),
}

// PublicIP accepts only public IP literals, never DNS names, paths, or ports.
func PublicIP(value string) string {
	ip, err := netip.ParseAddr(strings.TrimSpace(value))
	if err != nil || ip.Zone() != "" {
		return ""
	}
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return ""
	}
	for _, prefix := range nonPublicPrefixes {
		if prefix.Contains(ip) {
			return ""
		}
	}
	return ip.String()
}
