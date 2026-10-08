package panelmigration

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func isolatedSSH(t *testing.T, execute func(ssh.Channel, string)) (Target, *atomic.Int32) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	authentications := &atomic.Int32{}
	configuration := &ssh.ServerConfig{PasswordCallback: func(conn ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
		authentications.Add(1)
		if conn.User() != "root" || string(password) != "isolated-password" {
			return nil, io.EOF
		}
		return nil, nil
	}}
	configuration.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				server, channels, requests, err := ssh.NewServerConn(conn, configuration)
				if err != nil {
					return
				}
				defer server.Close()
				go ssh.DiscardRequests(requests)
				for incoming := range channels {
					if incoming.ChannelType() != "session" {
						_ = incoming.Reject(ssh.UnknownChannelType, "session only")
						continue
					}
					channel, requests, err := incoming.Accept()
					if err != nil {
						return
					}
					for request := range requests {
						if request.Type != "exec" {
							_ = request.Reply(false, nil)
							continue
						}
						var payload struct{ Command string }
						if ssh.Unmarshal(request.Payload, &payload) != nil {
							_ = request.Reply(false, nil)
							continue
						}
						_ = request.Reply(true, nil)
						execute(channel, payload.Command)
						_ = channel.Close()
						break
					}
				}
			}()
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); workers.Wait() })
	host, rawPort, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(rawPort)
	return Target{Host: host, Port: port, Fingerprint: ssh.FingerprintSHA256(signer.PublicKey())}, authentications
}

func TestSSHProbeAndPinnedStdinExecution(t *testing.T) {
	input := RemoteInput{Action: "restore", ID: testID, Password: "isolated-backup-secret", Archive: []byte("encrypted fixture")}
	target, authentications := isolatedSSH(t, func(channel ssh.Channel, command string) {
		if !strings.Contains(command, shellLiteral(remoteProgram)) || strings.Contains(command, input.Password) || strings.Contains(command, "isolated-password") {
			t.Error("executor command changed or contains credentials")
		}
		var received RemoteInput
		if err := json.NewDecoder(channel).Decode(&received); err != nil {
			t.Error(err)
		}
		if received.ID != input.ID || received.Password != input.Password || string(received.Archive) != string(input.Archive) {
			t.Error("stdin migration payload changed")
		}
		_, _ = io.WriteString(channel.Stderr(), "private remote diagnostic")
		_, _ = io.WriteString(channel, `{"state":"restored","recovery_id":"receipt"}`)
		_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	fingerprint, err := (SSHTransport{}).Probe(ctx, target)
	if err != nil || fingerprint != target.Fingerprint || authentications.Load() != 0 {
		t.Fatalf("probe sent credentials or failed: %v", err)
	}
	wrong := target
	wrong.Fingerprint = "SHA256:" + strings.Repeat("a", 43)
	if _, err := (SSHTransport{}).Run(ctx, wrong, Credentials{Password: "isolated-password"}, input); err == nil || authentications.Load() != 0 {
		t.Fatal("pin mismatch sent credentials")
	}
	result, err := (SSHTransport{}).Run(ctx, target, Credentials{Password: "isolated-password"}, input)
	if err != nil || result.State != "restored" || result.RecoveryID != "receipt" {
		t.Fatalf("real SSH stdin execution failed: %+v %v", result, err)
	}
	if _, err := (SSHTransport{}).Run(ctx, target, Credentials{Password: "wrong"}, input); err == nil || strings.Contains(err.Error(), "wrong") {
		t.Fatal("bad authentication accepted or leaked")
	}
}

func TestSSHExecutionCancellation(t *testing.T) {
	started := make(chan struct{})
	target, _ := isolatedSSH(t, func(channel ssh.Channel, _ string) {
		var received RemoteInput
		_ = json.NewDecoder(channel).Decode(&received)
		close(started)
		_, _ = io.Copy(io.Discard, channel)
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := (SSHTransport{}).Run(ctx, target, Credentials{Password: "isolated-password"}, RemoteInput{Action: "prepare", ID: testID})
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("SSH session did not start")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled execution succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled execution retained SSH connection")
	}
}

func TestSSHHandshakeCancellation(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			accepted <- conn
		}
	}()
	host, rawPort, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(rawPort)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := (SSHTransport{}).Probe(ctx, Target{Host: host, Port: port}); done <- err }()
	select {
	case conn := <-accepted:
		defer conn.Close()
	case <-time.After(time.Second):
		cancel()
		t.Fatal("connection did not start")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled handshake succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("SSH handshake ignored cancellation")
	}
}
