package diagnostics

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
)

type auditSink struct {
	events []audit.Event
	err    error
}

func (a *auditSink) AppendAudit(_ context.Context, event audit.Event) error {
	if a.err != nil {
		return a.err
	}
	a.events = append(a.events, event)
	return nil
}

type staticResolver struct{ addrs []net.IPAddr }

func (r staticResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return r.addrs, nil
}

type spyPinger struct {
	called int
	ip     netip.Addr
}

func (p *spyPinger) Ping(_ context.Context, ip netip.Addr) error { p.called++; p.ip = ip; return nil }

func TestTargetsRejectPrivateAndMalformedHosts(t *testing.T) {
	for _, raw := range []string{"Private|127.0.0.1", "Private|10.0.0.1", "Private|fc00::1", "Port|example.com:22", "Cmd|example.com;id", "Local|localhost", "Bad|123456"} {
		if _, err := ParseTargets(raw); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
	targets, err := ParseTargets("DNS|one.one.one.one,IP|1.1.1.1")
	if err != nil || len(targets) != 2 || targets[0].ID != "target-1" {
		t.Fatalf("targets=%+v err=%v", targets, err)
	}
}

func TestRunPinsPublicAddressAndAudits(t *testing.T) {
	targets, _ := ParseTargets("DNS|one.one.one.one")
	sink := &auditSink{}
	pinger := &spyPinger{}
	now := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	svc := NewService(targets, audit.NewService(sink), staticResolver{addrs: []net.IPAddr{{IP: net.ParseIP("1.1.1.1")}}}, pinger, func() time.Time { return now })
	result, err := svc.Run(t.Context(), "admin-1", Request{TargetID: "target-1", Action: "ping"})
	if err != nil || result.Status != "ok" || pinger.called != 1 || pinger.ip.String() != "1.1.1.1" {
		t.Fatalf("result=%+v err=%v pinger=%+v", result, err, pinger)
	}
	if len(sink.events) != 2 || sink.events[0].Outcome != "requested" || sink.events[1].Outcome != "ok" {
		t.Fatalf("events=%+v", sink.events)
	}
	if _, err := svc.Run(t.Context(), "admin-1", Request{TargetID: "target-1", Action: "ping"}); err == nil || pinger.called != 1 {
		t.Fatal("rate limit did not prevent another ping")
	}
}

func TestRunRejectsDNSRebindingAndAuditFailure(t *testing.T) {
	targets, _ := ParseTargets("DNS|one.one.one.one")
	pinger := &spyPinger{}
	now := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	sink := &auditSink{}
	svc := NewService(targets, audit.NewService(sink), staticResolver{addrs: []net.IPAddr{{IP: net.ParseIP("1.1.1.1")}, {IP: net.ParseIP("192.168.1.1")}}}, pinger, func() time.Time { return now })
	result, err := svc.Run(t.Context(), "admin-1", Request{TargetID: "target-1", Action: "ping"})
	if err != nil || result.Status != "failed" || pinger.called != 0 || sink.events[1].Outcome != "failed" {
		t.Fatalf("result=%+v err=%v events=%+v", result, err, sink.events)
	}
	sink = &auditSink{err: errors.New("audit unavailable")}
	svc = NewService(targets, audit.NewService(sink), staticResolver{addrs: []net.IPAddr{{IP: net.ParseIP("1.1.1.1")}}}, pinger, func() time.Time { return now })
	if _, err := svc.Run(t.Context(), "admin-1", Request{TargetID: "target-1", Action: "ping"}); err == nil || pinger.called != 0 {
		t.Fatal("executed without durable audit")
	}
}
