package panellicenseissuer

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	p "github.com/hongle/hl-panel/internal/panellicense"
)

func redemptionTestFields() (string, string, string) {
	return "redeem.example.test", p.InstallProofPath + "redeem_" + strings.Repeat("a", 32), "hl-license-v2=" + strings.Repeat("b", 32)
}

func redemptionTestCertificate(t *testing.T, domain string) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{domain},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, pub, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, roots
}

func redemptionTLSServer(t *testing.T, domain string, handler http.Handler) (*httptest.Server, *x509.CertPool) {
	t.Helper()
	cert, roots := redemptionTestCertificate(t, domain)
	server := httptest.NewUnstartedServer(handler)
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server, roots
}

func TestRedemptionProofRealTLSPinsDNSHostSNIAndIgnoresProxy(t *testing.T) {
	domain, path, value := redemptionTestFields()
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	var hits, lookups, dials atomic.Int32
	server, roots := redemptionTLSServer(t, domain, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Host != domain || r.RequestURI != path || r.Method != http.MethodGet ||
			r.TLS == nil || r.TLS.ServerName != domain || r.TLS.Version < tls.VersionTLS12 ||
			r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("HTTPS proof lost its domain/TLS boundary or attached credentials")
		}
		fmt.Fprint(w, value)
	}))
	v := &httpProofVerifier{roots: roots,
		resolver: proofResolverFunc(func(_ context.Context, network, host string) ([]netip.Addr, error) {
			lookups.Add(1)
			if network != "ip" || host != domain {
				t.Error("unexpected proof DNS query")
			}
			return []netip.Addr{netip.MustParseAddr("::ffff:8.8.8.8")}, nil
		}),
		dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			dials.Add(1)
			if network != "tcp" || address != "8.8.8.8:443" {
				t.Errorf("HTTPS dial was not pinned: %s %s", network, address)
			}
			return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
		},
	}
	ips, err := v.VerifyRedemptionProof(context.Background(), domain, path, value)
	if err != nil || len(ips) != 1 || ips[0] != "8.8.8.8" || hits.Load() != 1 || lookups.Load() != 1 || dials.Load() != 1 {
		t.Fatalf("HTTPS proof IP/count mismatch: %v %v %d/%d/%d", ips, err, hits.Load(), lookups.Load(), dials.Load())
	}
}

func TestRedemptionProofRejectsUntrustedOrWrongDomainTLS(t *testing.T) {
	domain, path, value := redemptionTestFields()
	for _, tc := range []struct {
		name, certificateDomain string
		trusted                 bool
	}{
		{"untrusted", domain, false},
		{"wrong-domain", "someone-else.example.test", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			server, roots := redemptionTLSServer(t, tc.certificateDomain, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits.Add(1)
				fmt.Fprint(w, value)
			}))
			if !tc.trusted {
				roots = x509.NewCertPool()
			}
			v := &httpProofVerifier{roots: roots,
				resolver: proofResolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
					return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
				}),
				dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
				},
			}
			if ips, err := v.VerifyRedemptionProof(context.Background(), domain, path, value); !errors.Is(err, ErrHTTP) || len(ips) != 0 || hits.Load() != 0 {
				t.Fatalf("unsafe certificate reached proof handler: %v %v %d", ips, err, hits.Load())
			}
		})
	}
}

func TestRedemptionProofRejectsRedirectAndUnsafeDNS(t *testing.T) {
	domain, path, value := redemptionTestFields()
	var hits, dials atomic.Int32
	server, roots := redemptionTLSServer(t, domain, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, "https://another.example.test"+path, http.StatusFound)
	}))
	v := &httpProofVerifier{roots: roots,
		resolver: proofResolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
		}),
		dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			dials.Add(1)
			return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
		},
	}
	if _, err := v.VerifyRedemptionProof(context.Background(), domain, path, value); !errors.Is(err, ErrHTTP) || hits.Load() != 1 || dials.Load() != 1 {
		t.Fatal("HTTPS proof followed a redirect", err)
	}
	v.resolver = proofResolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("127.0.0.1")}, nil
	})
	if _, err := v.VerifyRedemptionProof(context.Background(), domain, path, value); !errors.Is(err, ErrHTTP) || dials.Load() != 1 {
		t.Fatal("mixed private DNS answer reached dial", err)
	}
}

func TestRedemptionProofIPv6FailureLeavesTLSBudgetForIPv4(t *testing.T) {
	domain, path, value := redemptionTestFields()
	server, roots := redemptionTLSServer(t, domain, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, value) }))
	var dials atomic.Int32
	var firstDeadline time.Time
	v := &httpProofVerifier{roots: roots,
		resolver: proofResolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("2606:4700:4700::1111"), netip.MustParseAddr("8.8.8.8")}, nil
		}),
		dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Error("proof dial missing bounded deadline")
			}
			if dials.Add(1) == 1 {
				firstDeadline = deadline
				if address != "[2606:4700:4700::1111]:443" || time.Until(deadline) > 2500*time.Millisecond {
					t.Error("first address consumed the shared HTTPS budget")
				}
				return nil, errors.New("synthetic unreachable IPv6")
			}
			if address != "8.8.8.8:443" || !deadline.After(firstDeadline) || time.Until(deadline) > 5*time.Second {
				t.Error("IPv4 fallback lost the pinned address or shared deadline")
			}
			return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
		},
	}
	if ips, err := v.VerifyRedemptionProof(context.Background(), domain, path, value); err != nil || len(ips) != 1 || ips[0] != "8.8.8.8" || dials.Load() != 2 {
		t.Fatal("IPv4 HTTPS fallback failed", ips, err)
	}
}
