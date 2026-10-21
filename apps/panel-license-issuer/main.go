// The standalone authority deliberately has no dependency on the control API,
// forwarding engine, customer storage, or any HT/NY business adapter.
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"github.com/hongle/hl-panel/internal/panellicenseissuer"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func secretFile(name string) (string, error) {
	path := os.Getenv(name)
	if path == "" {
		return "", errors.New("required secret file is missing")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 8192 {
		return "", errors.New("invalid secret file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", errors.New("cannot read secret file")
	}
	value := strings.TrimSpace(string(raw))
	if value == "" {
		return "", errors.New("empty secret file")
	}
	return value, nil
}
func run() error {
	dsn, err := secretFile("HL_LICENSE_ISSUER_DATABASE_URL_FILE")
	if err != nil {
		return err
	}
	token, err := secretFile("HL_LICENSE_ISSUER_ADMIN_TOKEN_FILE")
	if err != nil {
		return err
	}
	encoded, err := secretFile("HL_LICENSE_ISSUER_SIGNING_KEY_FILE")
	if err != nil {
		return err
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return errors.New("invalid signing key encoding")
	}
	var key ed25519.PrivateKey
	switch len(raw) {
	case ed25519.SeedSize:
		key = ed25519.NewKeyFromSeed(raw)
	case ed25519.PrivateKeySize:
		key = ed25519.PrivateKey(raw)
	default:
		return errors.New("invalid signing key size")
	}
	address := os.Getenv("HL_LICENSE_ISSUER_LISTEN")
	if address == "" {
		address = "127.0.0.1:8712"
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return errors.New("issuer listener must bind a loopback IP behind an HTTPS proxy")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	initCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	store, err := panellicenseissuer.OpenPostgres(initCtx, dsn)
	cancel()
	if err != nil {
		return errors.New("issuer database initialization failed")
	}
	defer store.Close()
	service, err := panellicenseissuer.New(store, panellicenseissuer.Config{Issuer: os.Getenv("HL_LICENSE_ISSUER_ID"), KeyID: os.Getenv("HL_LICENSE_ISSUER_KEY_ID"), SigningKey: key, AdminToken: token})
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return errors.New("issuer listener failed")
	}
	server := &http.Server{Handler: service.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 * 1024}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	log.Print("standalone HL license authority started on loopback")
	select {
	case err := <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			return errors.New("issuer HTTP server failed")
		}
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if server.Shutdown(shutdown) != nil {
			return errors.New("issuer HTTP shutdown failed")
		}
	}
	return nil
}
func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
