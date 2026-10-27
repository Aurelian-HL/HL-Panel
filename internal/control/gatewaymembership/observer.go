package gatewaymembership

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/protocolprobe"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
	"github.com/hongle/hl-panel/internal/serviceaddress"
)

var (
	ErrProtocolObservationUnavailable = errors.New("VLESS protocol observation unavailable")
	ErrProtocolObservationTarget      = errors.New("VLESS protocol observation target mismatch")
)

// ProtocolObserver runs a real VLESS Reality challenge and publishes
// a short lease only when the challenge is tied to the current rule revision,
// node configuration generation, and configuration hash.
type ProtocolObserver struct {
	repository Repository
	clock      func() time.Time
	check      func(context.Context, protocolprobe.Spec) (time.Time, error)
}

type ProtocolProbeConfig struct {
	UUID     string
	EchoPort int
}

type ProtocolProbeConfigSource interface {
	ProtocolProbeConfig(context.Context, string) (ProtocolProbeConfig, error)
}

type ProtocolProbeConfigurator interface {
	ConfigureProtocolProbe(context.Context, string, ProtocolProbeConfig) error
}

func NewProtocolObserver(repository Repository, clock func() time.Time) *ProtocolObserver {
	if clock == nil {
		clock = time.Now
	}
	return &ProtocolObserver{repository: repository, clock: clock, check: protocolprobe.Check}
}

// ObserveNode performs one probe. The caller must provide a probe-only UUID
// already present in the node's deployed Xray inbound; this method rejects
// mismatched listener/echo targets and never uses a customer identity.
func (o *ProtocolObserver) ObserveNode(ctx context.Context, poolID, nodeID string, spec protocolprobe.Spec) (ProtocolObservation, error) {
	publisher, ok := o.repository.(ProtocolHealthPublisher)
	if !ok {
		return ProtocolObservation{}, ErrProtocolObservationUnavailable
	}
	state, err := o.repository.GatewayMembershipState(ctx, poolID)
	if err != nil {
		return ProtocolObservation{}, err
	}
	configSource, ok := o.repository.(ProtocolProbeConfigSource)
	if !ok {
		return ProtocolObservation{}, ErrProtocolObservationUnavailable
	}
	probeConfig, err := configSource.ProtocolProbeConfig(ctx, state.Rule.ID)
	if err != nil {
		return ProtocolObservation{}, err
	}
	observation, err := o.observeState(ctx, state, probeConfig, nodeID, spec)
	if err != nil {
		return ProtocolObservation{}, err
	}
	if err := publisher.PublishProtocolHealth(ctx, observation); err != nil {
		return ProtocolObservation{}, err
	}
	return observation, nil
}

// observeState reuses one consistent scan snapshot. Publishing must revalidate
// the current deployment in its transaction after the network challenge.
func (o *ProtocolObserver) observeState(ctx context.Context, state State, probeConfig ProtocolProbeConfig, nodeID string, spec protocolprobe.Spec) (ProtocolObservation, error) {
	rule := state.Rule
	if probeConfig.UUID == "" || probeConfig.UUID != spec.UUID || probeConfig.EchoPort != spec.EchoPort || spec.EffectiveFlow() != rule.EffectiveVLESSFlow() {
		return ProtocolObservation{}, ErrProtocolObservationTarget
	}
	if state.Pool.ID == "" || state.Pool.Protocol != "vless" || state.Pool.RuleID != rule.ID ||
		rule.ID == "" || (rule.Protocol != forwarding.ProtocolTCP && rule.Protocol != forwarding.ProtocolUDP) || rule.EffectiveIngressProtocol() != forwarding.IngressVLESSReality ||
		rule.Paused || (rule.Status != forwarding.StatusPendingActivation && rule.Status != forwarding.StatusActive) || rule.IngressReadiness() != forwarding.IngressReady {
		return ProtocolObservation{}, ErrProtocolObservationUnavailable
	}
	var member endpoints.EndpointPoolMember
	var found bool
	for _, candidate := range state.Members {
		if candidate.NodeID == nodeID {
			member, found = candidate, true
			break
		}
	}
	// A protocol probe is what establishes LastHealthAt. Requiring the old
	// lease here would deadlock members whose address/config changed and had
	// their previous lease deliberately cleared. Keep the structural checks
	// strict, then let PublishProtocolHealth restore the protocol-backed lease.
	if !found || member.PoolID != state.Pool.ID || member.GroupID != state.Pool.GroupID ||
		member.State != endpoints.CandidateEligible || member.Weight < 1 || member.ActiveConnections < 0 {
		return ProtocolObservation{}, ErrProtocolObservationUnavailable
	}
	receipt := state.Deployments[nodeID]
	if !deploymentReady(receipt, rule, nodeID) || receipt.Status.Engine != agentv1.EngineXray {
		return ProtocolObservation{}, fmt.Errorf("%w: deployment receipt not ready (runtime_revision=%d receipt_rule_revision=%d receipt_rule_id=%q receipt_node_id=%q verified=%t reason=%q engine=%q generation=%d hash_len=%d)", ErrProtocolObservationUnavailable, rule.EffectiveRuntimeRevision(), receipt.RuleRevision, receipt.Status.RuleID, receipt.Status.NodeID, receipt.Status.ReceiptVerified, receipt.Status.Reason, receipt.Status.Engine, receipt.Status.NodeConfigGeneration, len(receipt.Status.ConfigSHA256))
	}
	host, err := serviceaddress.NormalizeHost(member.DialHost)
	if err != nil || host == "" || spec.DialHost != host || spec.DialPort != rule.ListenPort || !loopbackHost(spec.EchoHost) {
		return ProtocolObservation{}, ErrProtocolObservationTarget
	}
	// An unrelated bundle generation must not invalidate every existing rule,
	// but changed rule content must prove connectivity again. Use the trusted
	// receipt fingerprint, never a caller-provided evidence key.
	spec.EvidenceKey = receipt.Status.RuleConfigSHA256
	if spec.EvidenceKey == "" {
		spec.EvidenceKey = receipt.Status.ConfigSHA256
	}
	verifiedAt, err := o.check(ctx, spec)
	if err != nil {
		return ProtocolObservation{}, err
	}
	now := o.clock().UTC()
	if verifiedAt.IsZero() || verifiedAt.After(now.Add(2*time.Second)) {
		return ProtocolObservation{}, ErrProtocolObservationUnavailable
	}
	observation := ProtocolObservation{
		RuleID: rule.ID, NodeID: nodeID, RuleRevision: rule.EffectiveRuntimeRevision(),
		NodeConfigGeneration: receipt.Status.NodeConfigGeneration, ConfigSHA256: receipt.Status.ConfigSHA256,
		RuleConfigSHA256: receipt.Status.RuleConfigSHA256,
		Protocol:         forwarding.IngressVLESSReality, DialHost: host, VerifiedAt: verifiedAt,
		LeaseExpiresAt: verifiedAt.Add(endpoints.DefaultHealthTTL),
	}
	return observation, nil
}

func loopbackHost(host string) bool {
	value := strings.TrimSpace(host)
	if value == "localhost" {
		return true
	}
	address, err := netip.ParseAddr(value)
	return err == nil && address.IsLoopback()
}
