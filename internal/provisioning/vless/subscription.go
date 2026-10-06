package vless

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// URI returns exactly one credential-bearing VLESS URI for the stable public
// endpoint. No membership input exists: scaling or replacing group machines
// therefore cannot change the customer's link.
func URI(profile Profile) (string, error) {
	if err := profile.Validate(); err != nil {
		return "", err
	}
	if profile.Reality != nil {
		return realityURI(profile)
	}
	query := url.Values{
		"encryption": {"none"}, "security": {"tls"},
		"type": {"tcp"}, "sni": {profile.TLS.ServerName}, "alpn": {"http/1.1"},
	}
	if profile.Identity.Flow != "" {
		query.Set("flow", profile.Identity.Flow)
	}
	link := url.URL{
		Scheme: "vless", User: url.User(strings.ToLower(profile.Identity.UUID)),
		Host:     net.JoinHostPort(profile.Endpoint.Hostname, strconv.Itoa(profile.Endpoint.Port)),
		RawQuery: query.Encode(), Fragment: profile.Endpoint.Name,
	}
	return link.String(), nil
}

func realityURI(profile Profile) (string, error) {
	reality := profile.Reality
	if reality == nil || reality.PublicKey == "" {
		return "", errors.New("Reality public key is required for URI generation")
	}
	if err := reality.validate(); err != nil {
		return "", err
	}
	query := url.Values{
		"encryption": {"none"}, "security": {"reality"}, "type": {"tcp"},
		"sni": {reality.ServerName}, "fp": {reality.Fingerprint},
		"pbk": {reality.PublicKey}, "sid": {reality.ShortID},
		"flow": {"xtls-rprx-vision"},
	}
	if reality.SpiderX != "" {
		query.Set("spx", reality.SpiderX)
	}
	link := url.URL{
		Scheme: "vless", User: url.User(strings.ToLower(profile.Identity.UUID)),
		Host:     net.JoinHostPort(profile.Endpoint.Hostname, strconv.Itoa(profile.Endpoint.Port)),
		RawQuery: query.Encode(), Fragment: profile.Endpoint.Name,
	}
	return link.String(), nil
}
