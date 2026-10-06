package gatewaymembership

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/protocolprobe"
)

type protocolRunnerRepository interface {
	Repository
	endpoints.Repository
	ProtocolProbeConfigSource
	ProtocolProbeConfigurator
}

// ProtocolRunner keeps VLESS Reality candidate health tied to the currently
// deployed node bundle. It is deliberately best-effort: a failed probe marks
// no health and never terminates the control API.
type ProtocolRunner struct {
	repository protocolRunnerRepository
	observer   *ProtocolObserver
	xrayBinary string
	echoPort   int
	interval   time.Duration
	logger     *slog.Logger
}

func NewProtocolRunner(repository protocolRunnerRepository, xrayBinary string, echoPort int, interval time.Duration, logger *slog.Logger) *ProtocolRunner {
	if logger == nil {
		logger = slog.Default()
	}
	if interval <= 0 {
		interval = 15 * time.Second
	}
	return &ProtocolRunner{
		repository: repository,
		observer:   NewProtocolObserver(repository, time.Now),
		xrayBinary: xrayBinary,
		echoPort:   echoPort,
		interval:   interval,
		logger:     logger,
	}
}

func (r *ProtocolRunner) Run(ctx context.Context) error {
	if err := r.scan(ctx); err != nil && !errors.Is(err, context.Canceled) {
		r.logger.Warn("VLESS protocol observation cycle failed", "error", err)
	}
	timer := time.NewTimer(r.interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			if err := r.scan(ctx); err != nil && !errors.Is(err, context.Canceled) {
				r.logger.Warn("VLESS protocol observation cycle failed", "error", err)
			}
			timer.Reset(r.interval)
		}
	}
}

func (r *ProtocolRunner) scan(ctx context.Context) error {
	started := time.Now()
	r.logger.Info("VLESS protocol observation scan started")
	pools, err := r.repository.ListEndpointPools(ctx)
	if err != nil {
		r.logger.Warn("VLESS protocol observation pool lookup failed", "elapsed", time.Since(started), "error", err)
		return err
	}
	r.logger.Info("VLESS protocol observation pools discovered", "count", len(pools), "elapsed", time.Since(started))
	for _, pool := range pools {
		if pool.Protocol != "vless" || pool.RuleID == "" {
			continue
		}
		if err := r.scanPool(ctx, pool); err != nil && !errors.Is(err, context.Canceled) {
			r.logger.Warn("VLESS protocol observation pool failed", "pool_id", pool.ID, "error", err)
		}
	}
	return nil
}

func (r *ProtocolRunner) scanPool(ctx context.Context, pool endpoints.EndpointPool) error {
	started := time.Now()
	r.logger.Info("VLESS protocol observation pool started", "pool_id", pool.ID, "rule_id", pool.RuleID)
	probe, err := r.repository.ProtocolProbeConfig(ctx, pool.RuleID)
	if errors.Is(err, faults.ErrNotFound) {
		probe, err = newProbeConfig(r.echoPort)
		if err != nil {
			return err
		}
		if err := r.repository.ConfigureProtocolProbe(ctx, pool.RuleID, probe); err != nil {
			if errors.Is(err, faults.ErrConflict) {
				return nil
			}
			return err
		}
		// The configuration mutation advances each node's desired generation.
		// Wait for the next cycle so probes cannot race an old bundle.
		r.logger.Info("VLESS protocol probe configured", "pool_id", pool.ID, "rule_id", pool.RuleID, "echo_port", probe.EchoPort, "elapsed", time.Since(started))
		return nil
	}
	if err != nil {
		r.logger.Warn("VLESS protocol probe configuration lookup failed", "pool_id", pool.ID, "rule_id", pool.RuleID, "elapsed", time.Since(started), "error", err)
		return err
	}
	state, err := r.repository.GatewayMembershipState(ctx, pool.ID)
	if err != nil {
		r.logger.Warn("VLESS protocol membership state lookup failed", "pool_id", pool.ID, "elapsed", time.Since(started), "error", err)
		return err
	}
	r.logger.Info("VLESS protocol observation members discovered", "pool_id", pool.ID, "rule_id", state.Rule.ID, "member_count", len(state.Members), "rule_status", state.Rule.Status, "ingress_status", state.Rule.IngressReadiness(), "deployed", state.Rule.Deployed, "elapsed", time.Since(started))
	for _, member := range state.Members {
		nodeStarted := time.Now()
		r.logger.Info("VLESS protocol observation node started", "pool_id", pool.ID, "node_id", member.NodeID, "dial_host", member.DialHost)
		spec := protocolprobe.Spec{
			XrayBinary: r.xrayBinary,
			DialHost:   member.DialHost,
			DialPort:   state.Rule.ListenPort,
			UUID:       probe.UUID,
			ServerName: state.Rule.RealityServerName,
			PublicKey:  state.Rule.RealityPublicKey,
			ShortID:    state.Rule.RealityShortID,
			EchoHost:   "127.0.0.1",
			EchoPort:   probe.EchoPort,
		}
		if _, err := r.observer.ObserveNode(ctx, pool.ID, member.NodeID, spec); err != nil {
			r.logger.Warn("VLESS protocol observation failed", "pool_id", pool.ID, "node_id", member.NodeID, "dial_host", member.DialHost, "dial_port", state.Rule.ListenPort, "elapsed", time.Since(nodeStarted), "error", err)
		} else {
			r.logger.Info("VLESS protocol observation succeeded", "pool_id", pool.ID, "node_id", member.NodeID, "dial_host", member.DialHost, "elapsed", time.Since(nodeStarted))
		}
	}
	if writer, ok := r.repository.(interface {
		MarkActivated(context.Context, string, audit.Event) error
	}); ok {
		fresh, err := r.repository.GatewayMembershipState(ctx, pool.ID)
		if err == nil {
			ready := 0
			for _, member := range fresh.Members {
				if protocolHealthy(fresh.ProtocolHealth[member.NodeID], fresh.Deployments[member.NodeID].Status, fresh.Rule, member, member.DialHost, time.Now().UTC()) {
					ready++
				}
			}
			if ready > 0 && fresh.Rule.ID != "" {
				event, eventErr := audit.NewEvent(time.Now().UTC(), "system", "protocol-runner", "forwarding_rule.activate", "forwarding_rule", fresh.Rule.ID, "succeeded", map[string]any{"healthy_candidates": ready})
				if eventErr == nil {
					if activateErr := writer.MarkActivated(ctx, fresh.Rule.ID, event); activateErr != nil {
						r.logger.Warn("VLESS protocol rule activation failed", "pool_id", pool.ID, "rule_id", fresh.Rule.ID, "healthy_candidates", ready, "error", activateErr)
					} else {
						r.logger.Info("VLESS protocol rule activation succeeded", "pool_id", pool.ID, "rule_id", fresh.Rule.ID, "healthy_candidates", ready)
					}
				}
			}
		}
	}
	r.logger.Info("VLESS protocol observation pool finished", "pool_id", pool.ID, "rule_id", pool.RuleID, "elapsed", time.Since(started))
	return nil
}

func newProbeConfig(echoPort int) (ProtocolProbeConfig, error) {
	if echoPort < 1 || echoPort > 65535 {
		return ProtocolProbeConfig{}, fmt.Errorf("probe echo port is invalid")
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return ProtocolProbeConfig{}, fmt.Errorf("generate protocol probe identity: %w", err)
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(raw[:])
	return ProtocolProbeConfig{UUID: fmt.Sprintf("%s-%s-%s-%s-%s", encoded[0:8], encoded[8:12], encoded[12:16], encoded[16:20], encoded[20:32]), EchoPort: echoPort}, nil
}
