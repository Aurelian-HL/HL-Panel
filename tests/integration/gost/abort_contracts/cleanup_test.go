package quiccleanupreview

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
)

func pair(t *testing.T) (*quic.Conn, *quic.Conn) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	if err != nil {
		t.Fatal(err)
	}
	ln, err := quic.ListenAddr("127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"cleanup-review"}}, &quic.Config{InitialStreamReceiveWindow: 16 * 1024, MaxStreamReceiveWindow: 16 * 1024, MaxIncomingStreams: 8})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	type result struct {
		c   *quic.Conn
		err error
	}
	accepted := make(chan result, 1)
	go func() { c, err := ln.Accept(ctx); accepted <- result{c, err} }()
	client, err := quic.DialAddr(ctx, ln.Addr().String(), &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"cleanup-review"}}, &quic.Config{})
	if err != nil {
		t.Fatal(err)
	}
	r := <-accepted
	if r.err != nil {
		t.Fatal(r.err)
	}
	t.Cleanup(func() { client.CloseWithError(0, "test complete"); r.c.CloseWithError(0, "test complete") })
	return client, r.c
}

func streams(t *testing.T, client, server *quic.Conn) (*quic.Stream, *quic.Stream) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	c, err := client.OpenStreamSync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	s, err := server.AcceptStream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 1)
	if _, err = io.ReadFull(s, b); err != nil {
		t.Fatal(err)
	}
	return c, s
}

func sibling(t *testing.T, client, server *quic.Conn) {
	t.Helper()
	c, s := streams(t, client, server)
	defer c.CancelRead(101)
	defer s.CancelRead(101)
	c.SetDeadline(time.Now().Add(time.Second))
	s.SetDeadline(time.Now().Add(time.Second))
	if _, err := s.Write([]byte("sibling")); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(c)
	if err != nil || string(b) != "sibling" {
		t.Fatalf("sibling payload=%q err=%v", b, err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	b, err = io.ReadAll(s)
	if err != nil || len(b) != 0 {
		t.Fatalf("sibling final read=%q err=%v", b, err)
	}
}

func TestNormalFINDoesNotCancelReverse(t *testing.T) {
	client, server := pair(t)
	c, s := streams(t, client, server)
	c.SetDeadline(time.Now().Add(time.Second))
	s.SetDeadline(time.Now().Add(time.Second))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("send Context did not cancel")
	}
	if _, err := io.ReadAll(c); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Write([]byte("reverse-after-fin")); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(s)
	if err != nil || string(b) != "reverse-after-fin" {
		t.Fatalf("reverse=%q err=%v", b, err)
	}
	select {
	case <-server.Context().Done():
		t.Fatal("FIN canceled session")
	default:
	}
	sibling(t, client, server)
}

func TestNativeCloseLeavesReadBlocked(t *testing.T) {
	client, server := pair(t)
	_, s := streams(t, client, server)
	done := make(chan error, 1)
	go func() { _, err := s.Read(make([]byte, 1)); done <- err }()
	time.Sleep(20 * time.Millisecond)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		t.Fatalf("Close unexpectedly unblocked Read: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	s.CancelRead(102)
	select {
	case err := <-done:
		var se *quic.StreamError
		if !errors.As(err, &se) {
			t.Fatalf("want StreamError got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("CancelRead failed to unblock")
	}
	sibling(t, client, server)
}

func TestDeadlineUnblocksReadAndPreservesSibling(t *testing.T) {
	client, server := pair(t)
	_, s := streams(t, client, server)
	done := make(chan error, 1)
	go func() { _, err := s.Read(make([]byte, 1)); done <- err }()
	time.Sleep(20 * time.Millisecond)
	s.SetDeadline(time.Now())
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected timeout")
		}
	case <-time.After(time.Second):
		t.Fatal("deadline failed to unblock")
	}
	s.CancelRead(103)
	s.CancelWrite(103)
	sibling(t, client, server)
}

func TestAbortUnblocksBothDirectionsAndPreservesSibling(t *testing.T) {
	client, server := pair(t)
	_, s := streams(t, client, server)
	readDone := make(chan error, 1)
	writeDone := make(chan error, 1)
	go func() { _, err := s.Read(make([]byte, 1)); readDone <- err }()
	go func() { _, err := s.Write(make([]byte, 8*1024*1024)); writeDone <- err }()
	time.Sleep(50 * time.Millisecond)
	select {
	case err := <-readDone:
		t.Fatalf("read already returned %v", err)
	default:
	}
	select {
	case err := <-writeDone:
		t.Fatalf("write not blocked %v", err)
	default:
	}
	s.CancelRead(104)
	s.CancelWrite(104)
	for name, ch := range map[string]<-chan error{"read": readDone, "write": writeDone} {
		select {
		case err := <-ch:
			var se *quic.StreamError
			if !errors.As(err, &se) {
				t.Fatalf("%s want StreamError got %v", name, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s abort did not unblock", name)
		}
	}
	sibling(t, client, server)
}
