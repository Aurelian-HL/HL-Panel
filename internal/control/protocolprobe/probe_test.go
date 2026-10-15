package protocolprobe

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"strconv"
	"testing"
	"time"
)

func validSpec() Spec {
	return Spec{
		DialHost: "node.example.com", DialPort: 443,
		UUID:       "123e4567-e89b-12d3-a456-426614174000",
		ServerName: "www.example.com",
		PublicKey:  "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		ShortID:    "0123456789abcdef", EchoHost: "echo.example.com", EchoPort: 8080,
	}
}

func TestClientConfigUsesRealityVisionAndLocalListener(t *testing.T) {
	data, err := clientConfig(validSpec(), 23001)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Inbounds []struct {
			Listen string `json:"listen"`
			Port   int    `json:"port"`
		} `json:"inbounds"`
		Outbounds []struct {
			Protocol string `json:"protocol"`
			Settings struct {
				VNext []struct {
					Address string `json:"address"`
					Users   []struct {
						Flow string `json:"flow"`
					} `json:"users"`
				} `json:"vnext"`
			} `json:"settings"`
			StreamSettings struct {
				Security string `json:"security"`
				Reality  struct {
					ServerName string `json:"serverName"`
					PublicKey  string `json:"publicKey"`
				} `json:"realitySettings"`
			} `json:"streamSettings"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Inbounds) != 1 || config.Inbounds[0].Listen != "127.0.0.1" || config.Inbounds[0].Port != 23001 ||
		len(config.Outbounds) != 1 || config.Outbounds[0].Protocol != "vless" ||
		config.Outbounds[0].Settings.VNext[0].Address != "node.example.com" ||
		config.Outbounds[0].Settings.VNext[0].Users[0].Flow != "xtls-rprx-vision" ||
		config.Outbounds[0].StreamSettings.Security != "reality" ||
		config.Outbounds[0].StreamSettings.Reality.ServerName != "www.example.com" ||
		config.Outbounds[0].StreamSettings.Reality.PublicKey != validSpec().PublicKey {
		t.Fatalf("incorrect Xray protocol configuration: %+v", config)
	}
}

func TestClientConfigRejectsIncompleteMaterial(t *testing.T) {
	tests := map[string]func(*Spec){
		"host":       func(s *Spec) { s.DialHost = "bad host" },
		"port":       func(s *Spec) { s.DialPort = 0 },
		"uuid":       func(s *Spec) { s.UUID = "secret" },
		"public key": func(s *Spec) { s.PublicKey = "private" },
		"short id":   func(s *Spec) { s.ShortID = "not-hex" },
		"echo host":  func(s *Spec) { s.EchoHost = "http://echo" },
		"echo port":  func(s *Spec) { s.EchoPort = 0 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			spec := validSpec()
			mutate(&spec)
			if _, err := clientConfig(spec, 23001); !errors.Is(err, ErrInvalidSpec) {
				t.Fatalf("invalid material accepted: %v", err)
			}
		})
	}
}

func TestProbeFlightDeduplicatesIdenticalChecks(t *testing.T) {
	resetProbeCacheForTests()
	key := probeCacheKey(validSpec(), "xray", fakeFileInfo{})
	first, owner := beginProbeFlight(key)
	if !owner {
		t.Fatal("first caller did not own probe flight")
	}
	second, owner := beginProbeFlight(key)
	if owner || second != first {
		t.Fatal("identical probe did not join existing flight")
	}
	want := time.Now().UTC()
	finishProbeFlight(key, first, want, nil)
	select {
	case <-second.done:
		if !second.verifiedAt.Equal(want) || second.err != nil {
			t.Fatalf("joined flight result = (%v, %v)", second.verifiedAt, second.err)
		}
	case <-time.After(time.Second):
		t.Fatal("joined probe did not complete")
	}
	third, owner := beginProbeFlight(key)
	if !owner || third == first {
		t.Fatal("completed flight was not removed")
	}
	finishProbeFlight(key, third, time.Time{}, ErrUnhealthy)
}

type fakeFileInfo struct{}

func (fakeFileInfo) Name() string       { return "xray" }
func (fakeFileInfo) Size() int64        { return 1 }
func (fakeFileInfo) Mode() os.FileMode  { return 0o755 }
func (fakeFileInfo) ModTime() time.Time { return time.Unix(1, 0) }
func (fakeFileInfo) IsDir() bool        { return false }
func (fakeFileInfo) Sys() any           { return nil }

func TestEchoThroughSOCKSRequiresMatchingChallenge(t *testing.T) {
	for _, echo := range []bool{true, false} {
		t.Run(map[bool]string{true: "echo", false: "wrong response"}[echo], func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			serverDone := make(chan error, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					serverDone <- err
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
				hello := make([]byte, 3)
				if _, err := io.ReadFull(conn, hello); err != nil {
					serverDone <- err
					return
				}
				_, _ = conn.Write([]byte{5, 0})
				request := make([]byte, 5+len("echo.example.com")+2)
				if _, err := io.ReadFull(conn, request); err != nil {
					serverDone <- err
					return
				}
				_, _ = conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 1})
				challenge := make([]byte, 32)
				if _, err := io.ReadFull(conn, challenge); err != nil {
					serverDone <- err
					return
				}
				if !echo {
					challenge[0] ^= 0xff
				}
				_, err = conn.Write(challenge)
				serverDone <- err
			}()
			conn, err := net.Dial("tcp", listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			err = echoThroughSOCKS(ctx, conn, "echo.example.com", 8080)
			if echo && err != nil || !echo && !errors.Is(err, ErrUnhealthy) {
				t.Fatalf("echo=%v, result=%v", echo, err)
			}
			if err := <-serverDone; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCheckRetriesWhenXrayExitsBeforeBindingPort(t *testing.T) {
	resetProbeCacheForTests()
	originalValidate, originalStart := validateXray, startXray
	t.Cleanup(func() {
		validateXray, startXray = originalValidate, originalStart
		resetProbeCacheForTests()
	})
	validateXray = func(context.Context, string, string) error { return nil }
	var attempts int
	startXray = func(_ context.Context, _ string, configPath string) (probeProcess, error) {
		attempts++
		if attempts == 1 {
			done := make(chan struct{})
			close(done)
			return probeProcess{done: done}, nil
		}
		var config struct {
			Inbounds []struct {
				Port int `json:"port"`
			} `json:"inbounds"`
		}
		data, err := os.ReadFile(configPath)
		if err != nil || json.Unmarshal(data, &config) != nil || len(config.Inbounds) != 1 {
			t.Fatalf("read probe config: %v", err)
		}
		listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(config.Inbounds[0].Port)))
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() {
			conn, acceptErr := listener.Accept()
			if acceptErr == nil {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
				hello := make([]byte, 3)
				if _, err := io.ReadFull(conn, hello); err == nil {
					_, _ = conn.Write([]byte{5, 0})
					request := make([]byte, 5+len("echo.example.com")+2)
					if _, err := io.ReadFull(conn, request); err == nil {
						_, _ = conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 1})
						challenge := make([]byte, 32)
						if _, err := io.ReadFull(conn, challenge); err == nil {
							_, _ = conn.Write(challenge)
						}
					}
				}
			}
			_ = listener.Close()
			close(done)
		}()
		return probeProcess{done: done, stop: func() { _ = listener.Close() }}, nil
	}
	spec := validSpec()
	spec.XrayBinary = os.Args[0]
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	verified, err := Check(ctx, spec)
	if err != nil || verified.IsZero() || attempts != 2 {
		t.Fatalf("Check() = (%v, %v), attempts=%d; want successful second attempt", verified, err, attempts)
	}
	second, err := Check(ctx, spec)
	if err != nil || second.IsZero() || attempts != 2 {
		t.Fatalf("cached Check() = (%v, %v), attempts=%d; want no new Xray process", second, err, attempts)
	}
}

func TestProbeCacheKeyChangesWhenEndpointChanges(t *testing.T) {
	resetProbeCacheForTests()
	info, err := os.Stat(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	first := probeCacheKey(validSpec(), os.Args[0], info)
	changed := validSpec()
	changed.DialPort++
	second := probeCacheKey(changed, os.Args[0], info)
	if first == second {
		t.Fatal("probe cache key did not change with endpoint material")
	}
}
