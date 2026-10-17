package gostconfig

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"time"
)

// ValidateTunnelTLS accepts inline, pinned certificates only. Native paths are
// authored exclusively by the agent after this validation.
func ValidateTunnelTLS(t *TLS, server bool) error {
	if t == nil || t.CertFile != "" || t.KeyFile != "" || t.CAFile != "" {
		return errors.New("inline tunnel TLS material is required")
	}
	raw := t.CAPEM
	if server {
		if t.CAPEM != "" || t.Secure || t.ServerName != "" {
			return errors.New("invalid server TLS options")
		}
		pair, err := tls.X509KeyPair([]byte(t.CertPEM), []byte(t.KeyPEM))
		if err != nil || len(pair.Certificate) != 1 {
			return errors.New("invalid tunnel certificate and private key")
		}
		raw = t.CertPEM
	} else if !t.Secure || t.ServerName != "hl-tunnel.internal" || t.CertPEM != "" || t.KeyPEM != "" {
		return errors.New("pinned verified tunnel TLS is required")
	}
	block, rest := pem.Decode([]byte(raw))
	if block == nil || block.Type != "CERTIFICATE" || len(rest) != 0 {
		return errors.New("invalid tunnel certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !cert.IsCA || !cert.BasicConstraintsValid {
		return errors.New("invalid tunnel CA")
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	_, err = cert.Verify(x509.VerifyOptions{Roots: roots, DNSName: "hl-tunnel.internal", CurrentTime: time.Now(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
	if err != nil {
		return errors.New("expired or invalid tunnel certificate")
	}
	return nil
}
