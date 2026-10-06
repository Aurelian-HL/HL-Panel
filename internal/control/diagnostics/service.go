package diagnostics

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"os/exec"
	"runtime"
	"sync"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/faults"
)

type Resolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

type Pinger interface {
	Ping(context.Context, netip.Addr) error
}

type Service struct {
	targets  []Target
	audit    *audit.Service
	resolver Resolver
	pinger   Pinger
	now      func() time.Time
	slots    chan struct{}
	mu       sync.Mutex
	lastRun  time.Time
}

type Request struct {
	TargetID string `json:"target_id"`
	Action   string `json:"action"`
}

type Result struct {
	TargetID  string    `json:"target_id"`
	Action    string    `json:"action"`
	Status    string    `json:"status"`
	CheckedAt time.Time `json:"checked_at"`
}

func NewService(targets []Target, auditService *audit.Service, resolver Resolver, pinger Pinger, now func() time.Time) *Service {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	if pinger == nil {
		pinger = systemPinger{}
	}
	if now == nil {
		now = time.Now
	}
	return &Service{targets: append([]Target(nil), targets...), audit: auditService, resolver: resolver, pinger: pinger, now: now, slots: make(chan struct{}, 2)}
}

func (s *Service) Targets() []Target {
	return append([]Target{}, s.targets...)
}

func (s *Service) Run(ctx context.Context, adminID string, input Request) (Result, error) {
	var target *Target
	for i := range s.targets {
		if s.targets[i].ID == input.TargetID {
			target = &s.targets[i]
			break
		}
	}
	if target == nil || (input.Action != "ping" && input.Action != "dns") {
		if err := s.RecordRejected(ctx, adminID); err != nil {
			return Result{}, err
		}
		return Result{}, fmt.Errorf("%w: unknown target or action", faults.ErrValidation)
	}
	if input.Action == "dns" {
		if _, err := netip.ParseAddr(target.host); err == nil {
			if err := s.RecordRejected(ctx, adminID); err != nil {
				return Result{}, err
			}
			return Result{}, fmt.Errorf("%w: DNS lookup requires a hostname", faults.ErrValidation)
		}
	}
	if err := s.record(ctx, adminID, *target, input.Action, "requested"); err != nil {
		return Result{}, err
	}
	s.mu.Lock()
	if s.now().Sub(s.lastRun) < 2*time.Second {
		s.mu.Unlock()
		if err := s.record(ctx, adminID, *target, input.Action, "rate_limited"); err != nil {
			return Result{}, err
		}
		return Result{}, fmt.Errorf("%w: wait before another diagnostic", faults.ErrConflict)
	}
	s.lastRun = s.now()
	s.mu.Unlock()
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		if err := s.record(ctx, adminID, *target, input.Action, "busy"); err != nil {
			return Result{}, err
		}
		return Result{}, fmt.Errorf("%w: diagnostic capacity reached", faults.ErrConflict)
	}
	checkContext, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	addr, err := s.resolve(checkContext, target.host)
	status := "ok"
	if err == nil && input.Action == "ping" {
		err = s.pinger.Ping(checkContext, addr)
	}
	if err != nil {
		status = "failed"
	}
	if auditErr := s.record(ctx, adminID, *target, input.Action, status); auditErr != nil {
		return Result{}, auditErr
	}
	return Result{TargetID: target.ID, Action: input.Action, Status: status, CheckedAt: s.now().UTC()}, nil
}

func (s *Service) RecordRejected(ctx context.Context, adminID string) error {
	event, err := audit.NewEvent(s.now().UTC(), "administrator", adminID, "diagnostics.run", "lookingglass_target", "unmatched", "denied", map[string]any{"scope": "control-plane-local"})
	if err != nil {
		return err
	}
	return s.audit.Record(ctx, event)
}

func (s *Service) resolve(ctx context.Context, host string) (netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		if publicIP(ip) {
			return ip.Unmap(), nil
		}
		return netip.Addr{}, fmt.Errorf("target resolved to a non-public IP")
	}
	addresses, err := s.resolver.LookupIPAddr(ctx, host)
	if err != nil || len(addresses) == 0 {
		return netip.Addr{}, fmt.Errorf("target lookup failed")
	}
	var selected netip.Addr
	for _, item := range addresses {
		ip, ok := netip.AddrFromSlice(item.IP)
		if !ok || !publicIP(ip) {
			return netip.Addr{}, fmt.Errorf("target resolved to a non-public IP")
		}
		if !selected.IsValid() {
			selected = ip.Unmap()
		}
	}
	return selected, nil
}

func (s *Service) record(ctx context.Context, adminID string, target Target, action, outcome string) error {
	event, err := audit.NewEvent(s.now().UTC(), "administrator", adminID, "diagnostics."+action, "lookingglass_target", target.ID, outcome, map[string]any{"scope": "control-plane-local"})
	if err != nil {
		return err
	}
	return s.audit.Record(ctx, event)
}

type systemPinger struct{}

func (systemPinger) Ping(ctx context.Context, ip netip.Addr) error {
	args := []string{"-n", "1", "-w", "2500", ip.String()}
	if runtime.GOOS != "windows" {
		args = []string{"-n", "-c", "1", "-W", "2", ip.String()}
	}
	return exec.CommandContext(ctx, "ping", args...).Run()
}
