// Package serviceaddress validates the one host used by a stable service
// endpoint. It does not perform DNS resolution or connect to the address.
package serviceaddress

import (
	"errors"
	"net/netip"
	"strings"
)

func NormalizeHost(value string) (string, error) {
	value = strings.TrimSpace(value)
	if ip, err := netip.ParseAddr(value); err == nil && ip.Zone() == "" {
		return ip.String(), nil
	}
	host := strings.ToLower(strings.TrimSuffix(value, "."))
	invalid := errors.New("host must be one DNS name or IP address, without credentials, protocol, path, or port")
	if host == "" || len(host) > 253 {
		return "", invalid
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", invalid
		}
		for _, char := range label {
			if !((char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '-') {
				return "", invalid
			}
		}
	}
	return host, nil
}
