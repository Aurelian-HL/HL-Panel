package panellicenseissuer

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"time"

	p "github.com/hongle/hl-panel/internal/panellicense"
)

// HTTPProofVerifier is injectable by embedded fixtures. Production command
// configuration never replaces the default verifier or enables private targets.
type HTTPProofVerifier interface {
	VerifyHTTPProof(ctx context.Context, domain, path, value string) error
}

type proofIPResolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

type httpProofVerifier struct {
	resolver proofIPResolver
	dial     func(context.Context, string, string) (net.Conn, error)
}

func newHTTPProofVerifier() *httpProofVerifier {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	return &httpProofVerifier{resolver: net.DefaultResolver, dial: dialer.DialContext}
}

// IsGlobalUnicast alone includes documentation, benchmark and transition
// networks. Explicitly exclude special-use ranges before making any connection.
var proofDeniedRanges = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.31.196.0/24"), netip.MustParsePrefix("192.52.193.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"), netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("192.175.48.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001::/23"), netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"), netip.MustParsePrefix("3fff::/20"),
	netip.MustParsePrefix("2620:4f:8000::/48"),
}

func publicProofIP(address netip.Addr) bool {
	if !address.IsValid() || address.Zone() != "" {
		return false
	}
	address = address.Unmap()
	if !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() {
		return false
	}
	if address.Is6() && !netip.MustParsePrefix("2000::/3").Contains(address) {
		return false
	}
	for _, prefix := range proofDeniedRanges {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

func (v *httpProofVerifier) VerifyHTTPProof(ctx context.Context, domain, path, value string) error {
	normalized, err := p.NormalizeDomain(domain)
	if err != nil || normalized != domain || len(path) != len(freeInstallHTTPPrefix)+37 ||
		len(value) != 46 || path[:len(freeInstallHTTPPrefix)] != freeInstallHTTPPrefix ||
		path[len(freeInstallHTTPPrefix):len(freeInstallHTTPPrefix)+5] != "free_" ||
		!validFreeNonce(path[len(freeInstallHTTPPrefix)+5:]) ||
		value[:14] != "hl-license-v2=" || !validFreeNonce(value[14:]) {
		return ErrHTTP
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	addresses, err := v.resolver.LookupNetIP(ctx, "ip", domain)
	if err != nil || len(addresses) == 0 || len(addresses) > 32 {
		return ErrHTTP
	}
	// Reject the entire DNS answer if even one address is unsafe. Resolve once,
	// then pin the checked address so DNS rebinding cannot change the dial target.
	for _, address := range addresses {
		if !publicProofIP(address) {
			return ErrHTTP
		}
	}
	// Give every validated address a chance within the same overall budget.
	// In particular, a black-holed IPv6 answer must not consume all five seconds
	// before an otherwise reachable IPv4 address can be attempted.
	deadline, _ := ctx.Deadline()
	for index, address := range addresses {
		if ctx.Err() != nil {
			return ErrHTTP
		}
		budget := time.Until(deadline) / time.Duration(len(addresses)-index)
		attemptCtx, attemptCancel := context.WithTimeout(ctx, budget)
		err := v.verifyHTTPProofAddress(attemptCtx, domain, path, value, address)
		attemptCancel()
		if err == nil {
			return nil
		}
	}
	return ErrHTTP
}

func (v *httpProofVerifier) verifyHTTPProofAddress(ctx context.Context, domain, path, value string, address netip.Addr) error {
	target := net.JoinHostPort(address.Unmap().String(), "80")
	transport := &http.Transport{
		Proxy: nil, DisableKeepAlives: true, DisableCompression: true,
		MaxResponseHeaderBytes: 4096, ResponseHeaderTimeout: 5 * time.Second,
		DialContext: func(_ context.Context, network, address string) (net.Conn, error) {
			if network != "tcp" || address != net.JoinHostPort(domain, "80") {
				return nil, errors.New("unexpected proof dial target")
			}
			// Transport may detach its dial context from the request deadline.
			// Use this attempt's bounded context for the actual pinned socket.
			return v.dial(ctx, "tcp", target)
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+domain+path, nil)
	if err != nil {
		return ErrHTTP
	}
	request.Header.Set("User-Agent", "HL-Panel-License-HTTP01/2")
	response, err := client.Do(request)
	if err != nil {
		return ErrHTTP
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ContentLength > 128 || response.Header.Get("Content-Encoding") != "" {
		return ErrHTTP
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 129))
	if err != nil || string(body) != value {
		return ErrHTTP
	}
	return nil
}
