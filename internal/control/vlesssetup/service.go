// Package vlesssetup connects a saved VLESS rule to its stable endpoint and
// identity. Engine deployment and protocol health remain separate evidence.
package vlesssetup

import (
	"context"
	"fmt"

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
	ListPoolsForAdministrator(context.Context, string) ([]endpoints.EndpointPool, error)
	CreateBound(context.Context, string, string, string, string, endpoints.Mode, string, string, int, endpoints.SelectionPolicy, string) (endpoints.EndpointPool, bool, error)
}

type IdentitySource interface {
	List(context.Context, string) ([]vlessidentity.Binding, error)
	Provision(context.Context, string, vlessidentity.ProvisionRequest, string) (vlessidentity.Binding, bool, error)
}

type Service struct {
	networks   NetworkSource
	endpoints  EndpointSource
	identities IdentitySource
}

func NewService(networks NetworkSource, endpointSource EndpointSource, identities IdentitySource) *Service {
	return &Service{networks: networks, endpoints: endpointSource, identities: identities}
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
		return faults.ErrNotFound
	}
	network, err := s.networks.Get(ctx, rule.EntryGroupID)
	if err != nil {
		return err
	}
	if network.ConnectHost == "" {
		return fmt.Errorf("%w: entry group has no stable address", faults.ErrValidation)
	}
	pools, err := s.endpoints.ListPoolsForAdministrator(ctx, administratorID)
	if err != nil {
		return err
	}
	var pool endpoints.EndpointPool
	for _, candidate := range pools {
		if candidate.RuleID == rule.ID {
			pool = candidate
			break
		}
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
