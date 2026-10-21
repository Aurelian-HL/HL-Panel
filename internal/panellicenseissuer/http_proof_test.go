package panellicenseissuer

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type proofResolverFunc func(context.Context, string, string) ([]netip.Addr, error)

func (f proofResolverFunc) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	return f(ctx, network, host)
}

func TestHTTPProofPublicAddressBoundaries(t *testing.T) {
	for _, value := range []string{"1.1.1.1", "8.8.8.8", "::ffff:8.8.8.8", "2606:4700:4700::1111", "2001:4860:4860::8888"} {
		if !publicProofIP(netip.MustParseAddr(value)) {
			t.Errorf("public address rejected: %s", value)
		}
	}
	for _, value := range []string{
		"0.0.0.0", "0.1.2.3", "10.1.2.3", "100.64.0.1", "100.127.255.254",
		"127.0.0.1", "169.254.169.254", "172.16.0.1", "172.31.255.255",
		"192.0.0.9", "192.0.2.1", "192.31.196.1", "192.52.193.1", "192.88.99.1",
		"192.168.1.1", "192.175.48.1", "198.18.0.1", "198.19.255.255", "198.51.100.1",
		"203.0.113.1", "224.0.0.1", "240.0.0.1", "255.255.255.255", "::", "::1",
		"::ffff:127.0.0.1", "::ffff:10.0.0.1", "::ffff:169.254.169.254", "::ffff:192.168.1.1",
		"64:ff9b::a00:1", "64:ff9b:1::1", "100::1", "2001::1", "2001:2::1", "2001:10::1",
		"2001:20::1", "2001:db8::1", "2002:7f00:1::1", "2620:4f:8000::1", "3fff::1",
		"fc00::1", "fd00::1", "fe80::1", "ff02::1", "4000::1", "2606:4700::1%eth0",
	} {
		if publicProofIP(netip.MustParseAddr(value)) {
			t.Errorf("unsafe address accepted: %s", value)
		}
	}
	if publicProofIP(netip.Addr{}) {
		t.Fatal("invalid address accepted")
	}
}

func proofTestFields() (string, string, string) {
	return "new-install.example.test", freeInstallHTTPPrefix + "free_" + strings.Repeat("a", 32), "hl-license-v2=" + strings.Repeat("b", 32)
}

func TestHTTPProofRealHTTPPinsDNSHostAndIgnoresProxy(t *testing.T) {
	domain, path, value := proofTestFields()
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	var hits, lookups, dials atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Host != domain || r.Method != http.MethodGet || r.RequestURI != path || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("proof request changed Host, method, path or attached credentials")
		}
		fmt.Fprint(w, value)
	}))
	defer server.Close()
	v := &httpProofVerifier{
		resolver: proofResolverFunc(func(ctx context.Context, network, host string) ([]netip.Addr, error) {
			lookups.Add(1)
			if network != "ip" || host != domain {
				t.Fatal("unexpected DNS query")
			}
			return []netip.Addr{netip.MustParseAddr("::ffff:8.8.8.8")}, nil
		}),
		dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			dials.Add(1)
			if network != "tcp" || address != "8.8.8.8:80" {
				t.Errorf("dial was not pinned to checked IP on port 80: %s %s", network, address)
			}
			return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
		},
	}
	if err := v.VerifyHTTPProof(context.Background(), domain, path, value); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 1 || lookups.Load() != 1 || dials.Load() != 1 {
		t.Fatalf("unexpected HTTP/DNS/dial counts: %d/%d/%d", hits.Load(), lookups.Load(), dials.Load())
	}
}

func TestHTTPProofRejectsUnsafeDNSBeforeDial(t *testing.T) {
	domain, path, value := proofTestFields()
	for _, item := range []struct {
		name string
		ips  []netip.Addr
		err  error
	}{
		{"no records", nil, nil}, {"DNS error", nil, errors.New("DNS failed")},
		{"mixed private", []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("127.0.0.1")}, nil},
		{"mixed mapped private", []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("::ffff:10.0.0.1")}, nil},
		{"reserved IPv6", []netip.Addr{netip.MustParseAddr("2001:db8::1")}, nil},
		{"excess answers", make([]netip.Addr, 33), nil},
	} {
		t.Run(item.name, func(t *testing.T) {
			v := &httpProofVerifier{resolver: proofResolverFunc(func(context.Context, string, string) ([]netip.Addr, error) { return item.ips, item.err }),
				dial: func(context.Context, string, string) (net.Conn, error) {
					t.Fatal("unsafe DNS reached dial")
					return nil, ErrHTTP
				}}
			if err := v.VerifyHTTPProof(context.Background(), domain, path, value); !errors.Is(err, ErrHTTP) {
				t.Fatalf("unsafe DNS accepted: %v", err)
			}
		})
	}
}

func TestHTTPProofFallsBackAcrossCheckedAddressesWithinOneBudget(t *testing.T) {
	domain, path, value := proofTestFields()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != domain || r.RequestURI != path {
			t.Error("fallback changed domain binding")
		}
		fmt.Fprint(w, value)
	}))
	defer server.Close()
	lookups, dials := 0, 0
	var firstDeadline time.Time
	v := &httpProofVerifier{
		resolver: proofResolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
			lookups++
			return []netip.Addr{netip.MustParseAddr("2606:4700:4700::1111"), netip.MustParseAddr("8.8.8.8")}, nil
		}),
		dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			dials++
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Error("fallback dial has no deadline")
			}
			if dials == 1 {
				if address != "[2606:4700:4700::1111]:80" {
					t.Errorf("first address not pinned: %s", address)
				}
				firstDeadline = deadline
				if time.Until(deadline) > 2500*time.Millisecond {
					t.Error("first address could consume the whole proof budget")
				}
				return nil, errors.New("synthetic unreachable IPv6")
			}
			if address != "8.8.8.8:80" || network != "tcp" {
				t.Errorf("fallback address not pinned: %s %s", network, address)
			}
			if !deadline.After(firstDeadline) || time.Until(deadline) > 5*time.Second {
				t.Error("fallback lost the shared deadline")
			}
			return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
		},
	}
	if err := v.VerifyHTTPProof(context.Background(), domain, path, value); err != nil {
		t.Fatal("reachable second address rejected", err)
	}
	if lookups != 1 || dials != 2 {
		t.Fatalf("fallback DNS/dial count: %d/%d", lookups, dials)
	}
}

func TestHTTPProofResponseBoundariesAndNoRedirect(t *testing.T) {
	domain, path, value := proofTestFields()
	var redirected atomic.Int32
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1); fmt.Fprint(w, value) }))
	defer redirectTarget.Close()
	for _, item := range []struct {
		name  string
		serve http.HandlerFunc
	}{
		{"redirect", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, redirectTarget.URL, http.StatusFound)
		}},
		{"not found", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404); fmt.Fprint(w, value) }},
		{"wrong token", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, strings.Repeat("z", 46)) }},
		{"HTML fallback", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "<html>"+value+"</html>") }},
		{"extra newline", func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, value) }},
		{"large body", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, strings.Repeat("x", 1024)) }},
		{"large chunked body", func(w http.ResponseWriter, r *http.Request) {
			w.(http.Flusher).Flush()
			fmt.Fprint(w, value+strings.Repeat("x", 1024))
		}},
		{"encoded body", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Encoding", "gzip")
			fmt.Fprint(w, value)
		}},
		{"huge header", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Large", strings.Repeat("x", 8192))
			fmt.Fprint(w, value)
		}},
	} {
		t.Run(item.name, func(t *testing.T) {
			server := httptest.NewServer(item.serve)
			defer server.Close()
			v := &httpProofVerifier{resolver: proofResolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
				return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
			}),
				dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
				}}
			if err := v.VerifyHTTPProof(context.Background(), domain, path, value); !errors.Is(err, ErrHTTP) {
				t.Fatalf("invalid HTTP response accepted: %v", err)
			}
		})
	}
	if redirected.Load() != 0 {
		t.Fatal("HTTP proof followed a redirect")
	}
}

func TestHTTPProofRejectsArbitraryPathsAndHasOneDeadline(t *testing.T) {
	domain, path, value := proofTestFields()
	v := &httpProofVerifier{resolver: proofResolverFunc(func(ctx context.Context, _, _ string) ([]netip.Addr, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 5*time.Second {
			t.Fatal("proof DNS missing bounded deadline")
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}), dial: func(context.Context, string, string) (net.Conn, error) {
		t.Fatal("expired DNS reached dial")
		return nil, ErrHTTP
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := v.VerifyHTTPProof(ctx, domain, path, value); !errors.Is(err, ErrHTTP) {
		t.Fatal("canceled DNS proof accepted", err)
	}
	v.resolver = proofResolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		t.Fatal("invalid proof reached DNS")
		return nil, ErrHTTP
	})
	for _, input := range [][3]string{{"127.0.0.1", path, value}, {domain + ":8080", path, value}, {domain, path + "?redirect=http://localhost", value}, {domain, "/../../admin", value}, {domain, "", value}, {domain, path, ""}, {domain, path, value[:45] + "="}} {
		if err := v.VerifyHTTPProof(context.Background(), input[0], input[1], input[2]); !errors.Is(err, ErrHTTP) {
			t.Fatal("arbitrary target or proof accepted", err)
		}
	}
}
