package protocolprobe

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
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
