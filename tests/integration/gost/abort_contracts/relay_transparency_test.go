package quiccleanupreview

import (
	"context"
	connector "github.com/go-gost/x/connector/relay"
	dialer "github.com/go-gost/x/dialer/quic"
	handler "github.com/go-gost/x/handler/relay"
	xnet "github.com/go-gost/x/internal/net"
	listener "github.com/go-gost/x/listener/quic"
	metrics "github.com/go-gost/x/metrics/wrapper"
	"net"
	"testing"
)

func TestActualRelayWrapperCancelReleasesCredit(t *testing.T) {
	for side, relay := range map[string]func(net.Conn) net.Conn{
		"connector": connector.NewReviewTCPConn, "handler": handler.NewReviewTCPConn,
	} {
		for adapter, wrap := range map[string]adapterFactory{
			"listener": listener.NewReviewConn, "dialer": dialer.NewReviewConn,
		} {
			t.Run(side+"/"+adapter, func(t *testing.T) {
				client, server := pair(t)
				for i := 0; i < 24; i++ {
					_, s := streams(t, client, server)
					q := metrics.WrapConn("review", relay(wrap(s, server)))
					a, b := tcpPair(t)
					ctx, cancel := context.WithCancel(context.Background())
					done := make(chan error, 1)
					go func() { done <- xnet.Pipe(ctx, q, a) }()
					cancel()
					waitPipe(t, done, context.Canceled)
					_ = b.Close()
				}
				sibling(t, client, server)
			})
		}
	}
}
