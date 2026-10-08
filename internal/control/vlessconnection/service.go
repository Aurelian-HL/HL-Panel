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

type unavailable struct{ reason string }

func (e unavailable) Error() string              { return faults.ErrConflict.Error() + ": " + e.reason }
func (e unavailable) Unwrap() error              { return faults.ErrConflict }
func (e unavailable) SubscriptionReason() string { return e.reason }

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
	if rule.Paused {
		return Connection{}, unavailable{"规则已暂停，请先恢复规则并等待节点应用"}
	}
	if rule.Status == forwarding.StatusQuotaExhausted {
		return Connection{}, unavailable{"规则流量额度已用完，请先调整额度并等待节点应用"}
	}
	if !rule.Deployed || rule.Status != forwarding.StatusActive {
		return Connection{}, unavailable{"规则尚未部署就绪，请检查节点在线状态与配置应用结果"}
	}
	if rule.EffectiveIngressProtocol() != forwarding.IngressVLESSReality || rule.Protocol != forwarding.ProtocolTCP || rule.VLESSFlow != "xtls-rprx-vision" || rule.RealityServerName == "" || rule.RealityPublicKey == "" || rule.RealityShortID == "" || rule.RealityDestination == "" {
		return Connection{}, unavailable{"Reality 参数不完整，请检查规则配置"}
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
		return Connection{}, unavailable{"设备组暂无健康在线节点，请检查节点状态"}
	}
	uri, err := provisioningvless.URI(provisioningvless.Profile{Endpoint: provisioningvless.Endpoint{Hostname: pool.Hostname, Port: pool.Port, Name: rule.Name}, Identity: provisioningvless.Identity{UUID: record.CredentialUUID, Flow: rule.VLESSFlow}, Reality: &provisioningvless.RealityProfile{ServerName: rule.RealityServerName, PublicKey: rule.RealityPublicKey, ShortID: rule.RealityShortID, Destination: rule.RealityDestination, Fingerprint: "chrome"}})
	if err != nil {
		return Connection{}, faults.ErrConflict
	}
	return Connection{URI: uri, Name: rule.Name, Endpoint: net.JoinHostPort(pool.Hostname, strconv.Itoa(pool.Port)), Status: "ready"}, nil
}
