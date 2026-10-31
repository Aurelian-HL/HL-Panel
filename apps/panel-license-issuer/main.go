// The standalone authority deliberately has no dependency on the control API,
// forwarding engine, customer storage, or any HT/NY business adapter.
package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	p "github.com/hongle/hl-panel/internal/panellicense"
	"github.com/hongle/hl-panel/internal/panellicenseissuer"
	"io"
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
	var exportKey []byte
	if os.Getenv("HL_LICENSE_ISSUER_EXPORT_KEY_FILE") != "" {
		encoded, err := secretFile("HL_LICENSE_ISSUER_EXPORT_KEY_FILE")
		if err != nil {
			return err
		}
		exportKey, err = base64.RawURLEncoding.DecodeString(encoded)
		if err != nil || len(exportKey) != 32 {
			return errors.New("invalid inventory export key")
		}
		defer clear(exportKey)
	}
	var products []p.ShopProduct
	if path := os.Getenv("HL_LICENSE_ISSUER_PRODUCTS_FILE"); path != "" {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 65536 {
			return errors.New("invalid shop products file")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return errors.New("cannot read shop products file")
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&products) != nil || decoder.Decode(new(any)) != io.EOF || products == nil {
			return errors.New("invalid shop products JSON")
		}
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
	service, err := panellicenseissuer.New(store, panellicenseissuer.Config{Issuer: os.Getenv("HL_LICENSE_ISSUER_ID"), KeyID: os.Getenv("HL_LICENSE_ISSUER_KEY_ID"), SigningKey: key, AdminToken: token, ExportKey: exportKey, Products: products})
	if err != nil {
		return err
	}
	purgeCtx, purgeCancel := context.WithTimeout(ctx, 5*time.Second)
	err = store.PurgeExpiredExports(purgeCtx, time.Now().UTC())
	purgeCancel()
	if err != nil {
		return errors.New("inventory export cleanup failed")
	}
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cleanup, cancel := context.WithTimeout(ctx, 5*time.Second)
				if store.PurgeExpiredExports(cleanup, time.Now().UTC()) != nil {
					log.Print("inventory export cleanup will retry")
				}
				cancel()
			}
		}
	}()
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
