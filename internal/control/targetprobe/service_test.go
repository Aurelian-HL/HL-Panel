package targetprobe

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"
)

func TestProbeReportsReachableLoopbackTarget(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr == nil {
			_ = connection.Close()
		}
	}()
	host, rawPort, _ := net.SplitHostPort(listener.Addr().String())
	var port int
	_, _ = fmt.Sscanf(rawPort, "%d", &port)
	result, err := New(time.Second).Probe(context.Background(), Request{Host: host, Port: port})
	if err != nil || !result.Reachable || result.ErrorCode != "" || result.Host != host || result.Port != port {
		t.Fatalf("unexpected probe result: %+v, err=%v", result, err)
	}
}

func TestProbeRejectsCredentialsAndPortInHost(t *testing.T) {
	for _, input := range []Request{{Host: "socks5://user:pass@127.0.0.1", Port: 80}, {Host: "127.0.0.1:80", Port: 80}, {Host: "127.0.0.1", Port: 0}} {
		if _, err := New(time.Second).Probe(context.Background(), input); err == nil {
			t.Fatalf("invalid probe request was accepted: %+v", input)
		}
	}
}
