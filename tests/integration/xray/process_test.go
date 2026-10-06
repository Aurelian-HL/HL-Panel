package xray_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func xrayBinary(t *testing.T) string {
	t.Helper()
	binary := os.Getenv("NYVP_XRAY_BINARY")
	if binary == "" {
		t.Skip("set NYVP_XRAY_BINARY to a verified local Xray binary to run real protocol integration")
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("NYVP_XRAY_BINARY must be absolute")
	}
	if info, err := os.Stat(binary); err != nil || !info.Mode().IsRegular() {
		t.Fatal("NYVP_XRAY_BINARY is not a regular executable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	version, err := exec.CommandContext(ctx, binary, "version").Output()
	if err != nil || !strings.HasPrefix(string(version), "Xray 26.3.27 ") {
		t.Fatal("real protocol test requires the verified pinned Xray 26.3.27 build")
	}
	return binary
}

func privateConfig(t *testing.T, data []byte) string {
	t.Helper()
	var diagnosticConfig map[string]any
	if err := json.Unmarshal(data, &diagnosticConfig); err != nil {
		t.Fatal(err)
	}
	diagnosticConfig["log"].(map[string]any)["loglevel"] = "debug"
	data, _ = json.Marshal(diagnosticConfig)
	file := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	return file
}

func startXray(t *testing.T, binary, config, address string) func() {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// Do not attach engine output to test logs: parsers can quote credentials.
	if err := exec.CommandContext(ctx, binary, "run", "-test", "-config", config).Run(); err != nil {
		t.Fatalf("pinned Xray rejected native configuration: %v", err)
	}
	process := exec.Command(binary, "run", "-config", config)
	var diagnostic bytes.Buffer
	process.Stdout, process.Stderr = &diagnostic, &diagnostic
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- process.Wait() }()
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		_ = process.Process.Kill()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Xray process did not stop")
			return
		}
		if t.Failed() {
			// Fixed markers only: raw engine output may contain identities or
			// configurations and must never be written to ordinary test logs.
			for _, marker := range []string{"unknown authority", "certificate", "handshake", "tls", "connection refused", "invalid request", "EOF", "failed to read request", "invalid user", "authentication", "not permitted", "failed", "rejected", "error", "timeout", "dialing TCP", "127.0.0.1", "SOCKS", "socks"} {
				if strings.Contains(diagnostic.String(), marker) {
					t.Logf("Xray diagnostic marker: %s", marker)
				}
			}
		}
	}
	t.Cleanup(stop)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		connection, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err == nil {
			connection.Close()
			return stop
		}
		select {
		case <-done:
			stopped = true
			t.Fatal("Xray exited before listener became ready")
		default:
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("Xray listener did not become ready")
	return stop
}

func freeLoopbackPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func localAddress(port int) string { return net.JoinHostPort("127.0.0.1", strconv.Itoa(port)) }

func testCertificate(t *testing.T) (certPath, keyPath, certPEM string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:        true, BasicConstraintsValid: true, DNSNames: []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	encodedKey, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	certPath, keyPath = filepath.Join(directory, "cert.pem"), filepath.Join(directory, "key.pem")
	certPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	if err := os.WriteFile(certPath, []byte(certPEM), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encodedKey}), 0600); err != nil {
		t.Fatal(err)
	}
	return
}
