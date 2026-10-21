// Package vlesssetup connects a saved VLESS rule to its stable endpoint and
// identity. Engine deployment and protocol health remain separate evidence.
package vlesssetup

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/groupconfig"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
)

type NetworkSource interface {
	Get(context.Context, string) (groupconfig.GroupNetwork, error)
}

type EndpointSource interface {
	FindPoolForRuleForAdministrator(context.Context, string, string) (endpoints.EndpointPool, error)
	CreateBound(context.Context, string, string, string, string, endpoints.Mode, string, string, int, endpoints.SelectionPolicy, string) (endpoints.EndpointPool, bool, error)
}

type IdentitySource interface {
	List(context.Context, string) ([]vlessidentity.Binding, error)
	Provision(context.Context, string, vlessidentity.ProvisionRequest, string) (vlessidentity.Binding, bool, error)
}

type Service struct {
	networks         NetworkSource
	endpoints        EndpointSource
	identities       IdentitySource
	reconcileTimeout time.Duration
	rules            RuleAccess
	wake             chan struct{}
	queueMu          sync.Mutex
	queue            []string
	queued           map[string]setupJob
	workers          int
}

// RuleAccess verifies delegated management using the durable create actor.
type RuleAccess interface {
	GetForAdministrator(context.Context, string, string) (forwarding.Rule, error)
	VLESSSetupAdministrator(context.Context, string) (string, error)
}

func WithRuleAccess(rules RuleAccess) Option {
	return func(s *Service) { s.rules = rules }
}

const defaultReconcileTimeout = 20 * time.Second

type Option func(*Service)

func WithReconcileTimeout(timeout time.Duration) Option {
	return func(service *Service) {
		if timeout > 0 {
			service.reconcileTimeout = timeout
		}
	}
}

func NewService(networks NetworkSource, endpointSource EndpointSource, identities IdentitySource, options ...Option) *Service {
	service := &Service{
		networks:         networks,
		endpoints:        endpointSource,
		identities:       identities,
		reconcileTimeout: defaultReconcileTimeout,
		wake:             make(chan struct{}, 1),
		queued:           make(map[string]setupJob),
	}
	for _, option := range options {
		if option != nil {
			option(service)
		}
	}
	return service
}

// Ensure is resumable after any intermediate write or a replayed rule save.
// The rule is not reported as deployed: node receipts and protocol probes own
// that transition.
func (s *Service) Ensure(ctx context.Context, administratorID string, rule forwarding.Rule) error {
	if rule.EffectiveIngressProtocol() != forwarding.IngressVLESSReality || rule.IngressReadiness() != forwarding.IngressReady ||
		rule.EgressMode != forwarding.EgressDirect || rule.PendingActivationReason() != forwarding.ActivationReasonVLESSRuntimeMaterialPending {
		return nil
	}
	if !rule.OwnedByAdministrator(administratorID) {
		if s.rules == nil {
			return faults.ErrNotFound
		}
		current, err := s.rules.GetForAdministrator(ctx, administratorID, rule.ID)
		if err != nil {
			return err
		}
		if current.CustomerID != rule.CustomerID || current.EntryGroupID != rule.EntryGroupID || current.ListenPort != rule.ListenPort {
			return faults.ErrConflict
		}
	}
	network, err := s.networks.Get(ctx, rule.EntryGroupID)
	if err != nil {
		return err
	}
	if network.ConnectHost == "" {
		return fmt.Errorf("%w: entry group has no stable address", faults.ErrValidation)
	}
	pool, err := s.endpoints.FindPoolForRuleForAdministrator(ctx, administratorID, rule.ID)
	if err != nil {
		return err
	}
	if pool.ID == "" {
		name := rule.Name
		if len(name) > 96 {
			name = name[:96]
		}
		pool, _, err = s.endpoints.CreateBound(ctx, administratorID, name+" "+rule.ID, rule.EntryGroupID, rule.ID,
			endpoints.ModeSingleServiceEndpoint, "vless", network.ConnectHost, rule.ListenPort,
			endpoints.SelectionWeightedRoundRobin, "automatic-endpoint:"+rule.ID)
		if err != nil {
			return err
		}
	}
	if pool.GroupID != rule.EntryGroupID || pool.Protocol != "vless" || pool.Port != rule.ListenPort || pool.Hostname != network.ConnectHost {
		return fmt.Errorf("%w: existing endpoint does not match the forwarding rule", faults.ErrConflict)
	}
	bindings, err := s.identities.List(ctx, administratorID)
	if err != nil {
		return err
	}
	for _, binding := range bindings {
		if binding.ForwardingRuleID == rule.ID && binding.State == vlessidentity.StateActive {
			if binding.EndpointPoolID != pool.ID || binding.CustomerID != rule.CustomerID {
				return fmt.Errorf("%w: existing VLESS identity does not match the stable endpoint", faults.ErrConflict)
			}
			return nil
		}
	}
	_, _, err = s.identities.Provision(ctx, administratorID, vlessidentity.ProvisionRequest{
		CustomerID: rule.CustomerID, ForwardingRuleID: rule.ID, EndpointPoolID: pool.ID,
	}, "automatic-identity:"+rule.ID)
	return err
}

type RuleSource interface {
	List(context.Context) ([]forwarding.Rule, error)
}

// Reconcile resumes committed rules after a cancelled request or a transient
// setup failure. Ensure uses the original idempotency keys and keeps identities.
func (s *Service) Reconcile(ctx context.Context, source RuleSource) error {
	rules, err := source.List(ctx)
	if err != nil {
		return err
	}
	var first error
	for _, rule := range rules {
		if err := ctx.Err(); err != nil {
			return err
		}
		if rule.Paused || rule.Status != forwarding.StatusPendingActivation ||
			(rule.ActivationReason != forwarding.ActivationReasonVLESSRuntimeMaterialPending &&
				!(rule.Protocol == forwarding.ProtocolUDP && rule.VLESSOutboundMode == forwarding.VLESSOutboundDirect &&
					rule.ActivationReason == forwarding.ActivationReasonUDPUnsupported)) {
			continue
		}
		actor := rule.OwnerID
		if rule.OwnerKind != forwarding.OwnerAdministrator {
			if s.rules == nil {
				continue
			}
			actor, err = s.rules.VLESSSetupAdministrator(ctx, rule.ID)
			if err != nil {
				if first == nil {
					first = err
				}
				continue
			}
			if actor == "" {
				continue
			}
		}
		if err := s.Ensure(ctx, actor, rule); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (s *Service) Run(ctx context.Context, source RuleSource, logger *slog.Logger) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		bounded, cancel := context.WithTimeout(ctx, s.reconcileTimeout)
		err := s.Reconcile(bounded, source)
		cancel()
		if err != nil && ctx.Err() == nil {
			logger.Warn("pending VLESS setup reconciliation failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.wake:
		}
	}
}
