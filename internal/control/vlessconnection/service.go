// Package vlessconnection projects currently authorized, healthy VLESS links.
package vlessconnection

import (
	"context"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
	provisioningvless "github.com/hongle/hl-panel/internal/provisioning/vless"
	"net"
	"strconv"
	"time"
)

type Service struct {
	identities *vlessidentity.Service
	forwarding *forwarding.Service
	endpoints  *endpoints.Service
}
type Connection struct {
	URI      string `json:"uri"`
	Name     string `json:"name"`
	Endpoint string `json:"endpoint"`
	Status   string `json:"status"`
}

func NewService(i *vlessidentity.Service, f *forwarding.Service, e *endpoints.Service) *Service {
	return &Service{i, f, e}
}
func (s *Service) ResolveSubscriptionLine(ctx context.Context, admin, customer, binding string) (string, error) {
	record, err := s.identities.CredentialByIDForAdministrator(ctx, admin, binding)
	if err != nil {
		return "", err
	}
	if record.Binding.CustomerID != customer && record.Binding.CustomerID != forwarding.AdministratorSubjectID(admin) {
		return "", faults.ErrNotFound
	}
	connection, err := s.Resolve(ctx, admin, record)
	return connection.URI, err
}
func (s *Service) Resolve(ctx context.Context, admin string, record vlessidentity.CredentialRecord) (Connection, error) {
	rule, err := s.forwarding.GetForAdministrator(ctx, admin, record.Binding.ForwardingRuleID)
	if err != nil {
		return Connection{}, err
	}
	if rule.EffectiveIngressProtocol() != forwarding.IngressVLESSReality || rule.Protocol != forwarding.ProtocolTCP || rule.Paused || !rule.Deployed || rule.Status != forwarding.StatusActive || rule.VLESSFlow != "xtls-rprx-vision" || rule.RealityServerName == "" || rule.RealityPublicKey == "" || rule.RealityShortID == "" || rule.RealityDestination == "" {
		return Connection{}, faults.ErrConflict
	}
	pools, err := s.endpoints.ListPoolsForAdministrator(ctx, admin)
	if err != nil {
		return Connection{}, err
	}
	var pool endpoints.EndpointPool
	for _, candidate := range pools {
		if candidate.ID == record.Binding.EndpointPoolID {
			pool = candidate
			break
		}
	}
	if pool.ID == "" {
		return Connection{}, faults.ErrNotFound
	}
	if pool.GroupID != rule.EntryGroupID || pool.RuleID != rule.ID || pool.Protocol != "vless" {
		return Connection{}, faults.ErrConflict
	}
	members, err := s.endpoints.CandidateSet(ctx, pool.ID, time.Now().UTC(), endpoints.DefaultHealthTTL)
	if err != nil {
		return Connection{}, err
	}
	if len(members) == 0 {
		return Connection{}, faults.ErrConflict
	}
	uri, err := provisioningvless.URI(provisioningvless.Profile{Endpoint: provisioningvless.Endpoint{Hostname: pool.Hostname, Port: pool.Port, Name: pool.Name}, Identity: provisioningvless.Identity{UUID: record.CredentialUUID, Flow: rule.VLESSFlow}, Reality: &provisioningvless.RealityProfile{ServerName: rule.RealityServerName, PublicKey: rule.RealityPublicKey, ShortID: rule.RealityShortID, Destination: rule.RealityDestination, Fingerprint: "chrome"}})
	if err != nil {
		return Connection{}, faults.ErrConflict
	}
	return Connection{URI: uri, Name: rule.Name, Endpoint: net.JoinHostPort(pool.Hostname, strconv.Itoa(pool.Port)), Status: "ready"}, nil
}
