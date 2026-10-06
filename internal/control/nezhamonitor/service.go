package nezhamonitor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/hongle/hl-panel/internal/control/faults"
)

const maxResponseBytes = 2 << 20

type Item struct {
	NodeID                    string     `json:"node_id"`
	NezhaServerID             *uint64    `json:"nezha_server_id,omitempty"`
	LinkStatus                string     `json:"link_status"`
	Online                    *bool      `json:"online,omitempty"`
	Name                      string     `json:"name,omitempty"`
	IPv4                      string     `json:"ipv4,omitempty"`
	IPv6                      string     `json:"ipv6,omitempty"`
	CountryCode               string     `json:"country_code,omitempty"`
	HostPlatform              string     `json:"host_platform,omitempty"`
	HostPlatformVersion       string     `json:"host_platform_version,omitempty"`
	HostArchitecture          string     `json:"host_architecture,omitempty"`
	HostBootTime              *uint64    `json:"host_boot_time,omitempty"`
	AgentVersion              string     `json:"agent_version,omitempty"`
	RegisteredAt              *time.Time `json:"registered_at,omitempty"`
	UptimeSeconds             *uint64    `json:"uptime_seconds,omitempty"`
	CPUPercent                *float64   `json:"cpu_percent,omitempty"`
	MemoryUsedBytes           *uint64    `json:"memory_used_bytes,omitempty"`
	MemoryTotalBytes          *uint64    `json:"memory_total_bytes,omitempty"`
	DiskUsedBytes             *uint64    `json:"disk_used_bytes,omitempty"`
	DiskTotalBytes            *uint64    `json:"disk_total_bytes,omitempty"`
	NetInSpeedBytesPerSecond  *uint64    `json:"net_in_speed_bytes_per_second,omitempty"`
	NetOutSpeedBytesPerSecond *uint64    `json:"net_out_speed_bytes_per_second,omitempty"`
	NetInTransferBytes        *uint64    `json:"net_in_transfer_bytes,omitempty"`
	NetOutTransferBytes       *uint64    `json:"net_out_transfer_bytes,omitempty"`
	TCPConnCount              *uint64    `json:"tcp_conn_count,omitempty"`
	UDPConnCount              *uint64    `json:"udp_conn_count,omitempty"`
	SampledAt                 *time.Time `json:"sampled_at,omitempty"`
}

type Service struct {
	endpoint string
	token    string
	mapping  map[string]uint64
	client   *http.Client
	now      func() time.Time
	bindings BindingSource
}

type BindingSource interface {
	ListNezhaBindings(context.Context) (map[string]uint64, error)
}

// SetBindingSource wires the durable HL node mapping before the HTTP server starts.
func (s *Service) SetBindingSource(source BindingSource) { s.bindings = source }

func New(baseURL, token string, mapping map[string]uint64, timeout time.Duration) (*Service, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.Port() == "" {
		return nil, errors.New("invalid Nezha loopback URL")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return nil, errors.New("Nezha URL must use a loopback IP literal")
	}
	if len(token) < 16 || len(token) > 512 || strings.ContainsAny(token, " \r\n\t") {
		return nil, errors.New("invalid Nezha PAT")
	}
	if timeout <= 0 || timeout > 15*time.Second {
		return nil, errors.New("invalid Nezha timeout")
	}
	if err := ValidateMapping(mapping); err != nil {
		return nil, err
	}
	u.Path = "/api/v1/server"
	return &Service{
		endpoint: u.String(), token: token, mapping: mapping,
		client: &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		now:    time.Now,
	}, nil
}

func Unlinked(nodeIDs []string) []Item {
	items := make([]Item, 0, len(nodeIDs))
	for _, nodeID := range nodeIDs {
		items = append(items, Item{NodeID: nodeID, LinkStatus: "unlinked"})
	}
	return items
}

func (s *Service) Fetch(ctx context.Context, nodeIDs []string) ([]Item, error) {
	items := Unlinked(nodeIDs)
	mapping, err := s.resolveMapping(ctx)
	if err != nil {
		return items, err
	}
	linked := false
	for i := range items {
		if id := mapping[items[i].NodeID]; id != 0 {
			items[i].NezhaServerID = &id
			items[i].LinkStatus = "linked"
			linked = true
		}
	}
	if !linked {
		return items, nil
	}
	servers, err := s.fetchServers(ctx)
	if err != nil {
		return items, err
	}
	for i := range items {
		if items[i].NezhaServerID == nil {
			continue
		}
		if entry, found := servers[*items[i].NezhaServerID]; found {
			s.project(&items[i], entry)
		}
	}
	return items, nil
}

// FetchInventory lists each Nezha server once, including agents not mapped to an HL node.
func (s *Service) FetchInventory(ctx context.Context) ([]Item, error) {
	mapping, err := s.resolveMapping(ctx)
	if err != nil {
		return nil, err
	}
	servers, err := s.fetchServers(ctx)
	if err != nil {
		return nil, err
	}
	nodesByServer := make(map[uint64]string, len(mapping))
	for nodeID, serverID := range mapping {
		nodesByServer[serverID] = nodeID
	}
	ids := make([]uint64, 0, len(servers))
	for id := range servers {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	items := make([]Item, 0, len(ids))
	for _, id := range ids {
		item := Item{NodeID: "nezha:" + fmt.Sprint(id), NezhaServerID: &id, LinkStatus: "unmanaged"}
		if nodeID := nodesByServer[id]; nodeID != "" {
			item.NodeID = nodeID
			item.LinkStatus = "linked"
		}
		s.project(&item, servers[id])
		items = append(items, item)
	}
	return items, nil
}

// ValidateAvailableServer only admits IDs visible through the read-only PAT.
// Local durable reservations are checked again inside the enrollment transaction.
func (s *Service) ValidateAvailableServer(ctx context.Context, serverID uint64) error {
	if serverID == 0 {
		return fmt.Errorf("%w: Nezha server ID is required", faults.ErrValidation)
	}
	mapping, err := s.resolveMapping(ctx)
	if err != nil {
		return err
	}
	for _, boundID := range mapping {
		if boundID == serverID {
			return fmt.Errorf("%w: Nezha server is already bound", faults.ErrConflict)
		}
	}
	servers, err := s.fetchServers(ctx)
	if err != nil {
		return err
	}
	if _, found := servers[serverID]; !found {
		return fmt.Errorf("%w: Nezha server is not visible", faults.ErrValidation)
	}
	return nil
}

func (s *Service) resolveMapping(ctx context.Context) (map[string]uint64, error) {
	merged := make(map[string]uint64, len(s.mapping))
	for nodeID, serverID := range s.mapping {
		merged[nodeID] = serverID
	}
	if s.bindings != nil {
		durable, err := s.bindings.ListNezhaBindings(ctx)
		if err != nil {
			return nil, err
		}
		for nodeID, serverID := range durable {
			merged[nodeID] = serverID
		}
	}
	if err := ValidateMapping(merged); err != nil {
		return nil, err
	}
	return merged, nil
}

func (s *Service) fetchServers(ctx context.Context) (map[uint64]server, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.endpoint, nil)
	if err != nil {
		return nil, errors.New("Nezha request failed")
	}
	request.Header.Set("Authorization", "Bearer "+s.token)
	request.Header.Set("Accept", "application/json")
	response, err := s.client.Do(request)
	if err != nil {
		return nil, errors.New("Nezha request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("Nezha response failed")
	}
	var payload serverResponse
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(raw) > maxResponseBytes {
		return nil, errors.New("invalid Nezha response")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&payload); err != nil || !payload.Success || payload.Data == nil {
		return nil, errors.New("invalid Nezha response")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("invalid Nezha response")
	}
	servers := make(map[uint64]server, len(payload.Data))
	for _, entry := range payload.Data {
		if entry.ID == 0 {
			return nil, errors.New("invalid Nezha response")
		}
		servers[entry.ID] = entry
	}
	return servers, nil
}

func (s *Service) project(item *Item, entry server) {
	now := s.now()
	item.Name = entry.Name
	item.IPv4 = entry.GeoIP.IP.IPv4Addr
	item.IPv6 = entry.GeoIP.IP.IPv6Addr
	item.CountryCode = entry.GeoIP.CountryCode
	if !entry.CreatedAt.IsZero() {
		t := entry.CreatedAt
		item.RegisteredAt = &t
	}
	online := !entry.LastActive.IsZero() && !entry.LastActive.After(now) && now.Sub(entry.LastActive) <= 30*time.Second
	item.Online = &online
	if !entry.LastActive.IsZero() {
		t := entry.LastActive
		item.SampledAt = &t
	}
	if entry.Host != nil {
		item.HostPlatform = entry.Host.Platform
		item.HostPlatformVersion = entry.Host.PlatformVersion
		item.HostArchitecture = entry.Host.Arch
		item.AgentVersion = entry.Host.Version
		if entry.Host.BootTime != 0 {
			bootTime := entry.Host.BootTime
			item.HostBootTime = &bootTime
		}
		item.MemoryTotalBytes = &entry.Host.MemTotal
		item.DiskTotalBytes = &entry.Host.DiskTotal
	}
	if entry.State != nil {
		item.UptimeSeconds = &entry.State.Uptime
		item.CPUPercent = &entry.State.CPU
		item.MemoryUsedBytes = &entry.State.MemUsed
		item.DiskUsedBytes = &entry.State.DiskUsed
		item.NetInSpeedBytesPerSecond = &entry.State.NetInSpeed
		item.NetOutSpeedBytesPerSecond = &entry.State.NetOutSpeed
		item.NetInTransferBytes = &entry.State.NetInTransfer
		item.NetOutTransferBytes = &entry.State.NetOutTransfer
		item.TCPConnCount = entry.State.TCPConnCount
		item.UDPConnCount = entry.State.UDPConnCount
	}
}

type serverResponse struct {
	Success bool     `json:"success"`
	Data    []server `json:"data"`
}

type server struct {
	ID         uint64    `json:"id"`
	Name       string    `json:"name"`
	CreatedAt  time.Time `json:"created_at"`
	LastActive time.Time `json:"last_active"`
	Host       *struct {
		Platform        string `json:"platform"`
		PlatformVersion string `json:"platform_version"`
		Arch            string `json:"arch"`
		BootTime        uint64 `json:"boot_time"`
		Version         string `json:"version"`
		MemTotal        uint64 `json:"mem_total"`
		DiskTotal       uint64 `json:"disk_total"`
	} `json:"host"`
	State *struct {
		CPU            float64 `json:"cpu"`
		MemUsed        uint64  `json:"mem_used"`
		DiskUsed       uint64  `json:"disk_used"`
		NetInSpeed     uint64  `json:"net_in_speed"`
		NetOutSpeed    uint64  `json:"net_out_speed"`
		NetInTransfer  uint64  `json:"net_in_transfer"`
		NetOutTransfer uint64  `json:"net_out_transfer"`
		TCPConnCount   *uint64 `json:"tcp_conn_count"`
		UDPConnCount   *uint64 `json:"udp_conn_count"`
		Uptime         uint64  `json:"uptime"`
	} `json:"state"`
	GeoIP struct {
		IP struct {
			IPv4Addr string `json:"ipv4_addr"`
			IPv6Addr string `json:"ipv6_addr"`
		} `json:"ip"`
		CountryCode string `json:"country_code"`
	} `json:"geoip"`
}

func ValidateMapping(mapping map[string]uint64) error {
	seen := make(map[uint64]string, len(mapping))
	for nodeID, serverID := range mapping {
		if nodeID == "" || strings.TrimSpace(nodeID) != nodeID || serverID == 0 {
			return errors.New("invalid Nezha node mapping")
		}
		if previous, ok := seen[serverID]; ok {
			return fmt.Errorf("Nezha server %d mapped to multiple nodes (%s, %s)", serverID, previous, nodeID)
		}
		seen[serverID] = nodeID
	}
	return nil
}
