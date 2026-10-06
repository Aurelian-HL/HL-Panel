package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/gateway/membership"
	"github.com/hongle/hl-panel/internal/routing/endpointrouter"
)

func TestRunLoadsFilesForwardsConnectionAndStops(t *testing.T) {
	backendListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backendListener.Close()
	backendDone := make(chan error, 1)
	go func() {
		connection, acceptErr := backendListener.Accept()
		if acceptErr != nil {
			backendDone <- acceptErr
			return
		}
		defer connection.Close()
		payload, readErr := io.ReadAll(connection)
		if readErr != nil {
			backendDone <- readErr
			return
		}
		_, writeErr := connection.Write(append([]byte("gateway:"), payload...))
		backendDone <- writeErr
	}()

	frontAddress := unusedLoopbackAddress(t)
	directory := t.TempDir()
	membershipPath := filepath.Join(directory, "membership.json")
	configurationPath := filepath.Join(directory, "config.json")
	writeJSONFile(t, membershipPath, membership.Snapshot{
		Revision: 1,
		Endpoints: []endpointrouter.Endpoint{{
			ID: "node-a", Address: backendListener.Addr().String(), Weight: 1,
			Status: endpointrouter.EndpointReady, HealthLeaseExpiresAt: time.Now().Add(time.Minute),
		}},
	})
	writeJSONFile(t, configurationPath, map[string]any{
		"listen_address": frontAddress, "membership_file": membershipPath,
		"membership_refresh_interval": "100ms", "dial_timeout": "1s",
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	go func() { runDone <- run(ctx, configurationPath, logger) }()

	client := dialWhenReady(t, frontAddress, runDone)
	if _, err := client.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if tcpClient, ok := client.(*net.TCPConn); ok {
		if err := tcpClient.CloseWrite(); err != nil {
			t.Fatal(err)
		}
	}
	response, err := io.ReadAll(client)
	if err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
	if string(response) != "gateway:hello" {
		t.Fatalf("response = %q, want gateway:hello", response)
	}
	select {
	case err := <-backendDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("backend did not finish")
	}
	cancel()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("run() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("run() did not stop after cancellation")
	}
}

func unusedLoopbackAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func writeJSONFile(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func dialWhenReady(t *testing.T, address string, runDone <-chan error) net.Conn {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		connection, err := net.DialTimeout("tcp", address, 50*time.Millisecond)
		if err == nil {
			return connection
		}
		select {
		case runErr := <-runDone:
			t.Fatalf("run() stopped before accepting connections: %v", runErr)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("connection gateway did not become ready")
	return nil
}
