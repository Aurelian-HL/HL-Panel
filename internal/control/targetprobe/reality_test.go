package targetprobe

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/faults"
)

func isolatedRealityProbe(t *testing.T, serverConfig *tls.Config) *RealityService {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "probe.example.test"}, DNSNames: []string{"probe.example.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	serverConfig.Certificates = []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			peer, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer peer.Close()
				_ = peer.SetDeadline(time.Now().Add(time.Second))
				_ = tls.Server(peer, serverConfig).Handshake()
			}()
		}
	}()
	s := NewReality(time.Second)
	s.tlsConfig.RootCAs = x509.NewCertPool()
	s.tlsConfig.RootCAs.AddCert(cert)
	s.lookup = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}
	s.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" || address != "8.8.8.8:443" {
			t.Errorf("dial not pinned to the validated address")
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp", listener.Addr().String())
	}
	return s
}

func TestRealityProbeTLSCompatibilityAndCertificateValidation(t *testing.T) {
	for _, test := range []struct {
		name       string
		config     *tls.Config
		sni        string
		trusted    bool
		compatible bool
	}{
		{"TLS13_X25519", &tls.Config{MinVersion: tls.VersionTLS13, CurvePreferences: []tls.CurveID{tls.X25519}, NextProtos: []string{"h2"}}, "probe.example.test", true, true},
		{"TLS12_only", &tls.Config{MaxVersion: tls.VersionTLS12}, "probe.example.test", true, false},
		{"wrong_SNI", &tls.Config{}, "other.example.test", true, false},
		{"untrusted_certificate", &tls.Config{}, "probe.example.test", false, false},
		{"no_X25519", &tls.Config{CurvePreferences: []tls.CurveID{tls.CurveP256}}, "probe.example.test", true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := isolatedRealityProbe(t, test.config)
			if !test.trusted {
				s.tlsConfig.RootCAs = x509.NewCertPool()
			}
			result, err := s.Probe(context.Background(), RealityRequest{ServerName: test.sni, Destination: "probe.example.test:443"})
			if err != nil || result.Compatible != test.compatible || result.Source != "control_plane" || result.CheckedAt.IsZero() {
				t.Fatalf("probe = %+v, err=%v", result, err)
			}
			if test.compatible && (result.TLSVersion != "TLS 1.3" || result.KeyExchange != "X25519" || result.ALPN != "h2") {
				t.Fatalf("incorrect TLS result: %+v", result)
			}
			if !test.compatible && result.ErrorCode != "tls_failed" {
				t.Fatalf("incorrect error: %+v", result)
			}
		})
	}
}

func TestRealityProbeRejectsPrivateTargetsAndDNSRebinding(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "::1", "fc00::1", "::ffff:127.0.0.1", "64:ff9b::7f00:1", "192.0.2.1"} {
		t.Run(ip, func(t *testing.T) {
			s := NewReality(time.Second)
			s.lookup = func(context.Context, string, string) ([]netip.Addr, error) {
				return []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr(ip)}, nil
			}
			s.dial = func(context.Context, string, string) (net.Conn, error) {
				t.Fatal("private or mixed DNS target was dialed")
				return nil, nil
			}
			result, err := s.Probe(context.Background(), RealityRequest{ServerName: "probe.example.test"})
			if err != nil || result.Compatible || result.ErrorCode != "non_public_target" {
				t.Fatalf("probe=%+v err=%v", result, err)
			}
		})
	}
}

func TestRealityProbeTimeoutAndConcurrencyBound(t *testing.T) {
	s := NewReality(30 * time.Millisecond)
	s.lookup = func(ctx context.Context, _, _ string) ([]netip.Addr, error) { <-ctx.Done(); return nil, ctx.Err() }
	start := time.Now()
	result, err := s.Probe(context.Background(), RealityRequest{ServerName: "probe.example.test"})
	if err != nil || result.ErrorCode != "timeout" || time.Since(start) > time.Second {
		t.Fatalf("unbounded timeout: %+v err=%v", result, err)
	}
	for i := 0; i < cap(s.slots); i++ {
		s.slots <- struct{}{}
	}
	if _, err := s.Probe(context.Background(), RealityRequest{ServerName: "probe.example.test"}); !errors.Is(err, faults.ErrConflict) {
		t.Fatal("probe concurrency is not bounded", err)
	}
}

func TestRealityProbeFallsBackWithoutReresolving(t *testing.T) {
	s := isolatedRealityProbe(t, &tls.Config{})
	lookupCalls, dialCalls := 0, 0
	s.lookup = func(context.Context, string, string) ([]netip.Addr, error) {
		lookupCalls++
		return []netip.Addr{netip.MustParseAddr("2001:4860:4860::8888"), netip.MustParseAddr("8.8.8.8")}, nil
	}
	dial := s.dial
	s.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		dialCalls++
		if dialCalls == 1 {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return dial(ctx, network, address)
	}
	result, err := s.Probe(context.Background(), RealityRequest{ServerName: "probe.example.test"})
	if err != nil || !result.Compatible || lookupCalls != 1 || dialCalls != 2 {
		t.Fatalf("fallback failed: %+v err=%v lookups=%d dials=%d", result, err, lookupCalls, dialCalls)
	}
}

func TestRealityProbeRejectsMalformedRequestsBeforeDNS(t *testing.T) {
	s := NewReality(time.Second)
	s.lookup = func(context.Context, string, string) ([]netip.Addr, error) {
		t.Fatal("invalid request reached DNS")
		return nil, nil
	}
	for _, request := range []RealityRequest{{}, {ServerName: "https://example.com"}, {ServerName: "example.com", Destination: "user:secret@example.com:443"}, {ServerName: "example.com", Destination: "example.com:0"}, {ServerName: "example.com", Destination: "example.com"}} {
		if _, err := s.Probe(context.Background(), request); !errors.Is(err, faults.ErrValidation) {
			t.Fatalf("accepted malformed request: %v", err)
		}
	}
}
