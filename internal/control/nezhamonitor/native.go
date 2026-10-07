package nezhamonitor

import (
	"encoding/json"
	"net/netip"
	"time"

	"github.com/hongle/hl-panel/internal/control/hostgeo"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

// MergeNative projects persisted HL heartbeats. Nezha status applies only to
// Nezha readings; an outage there must not hide a healthy native HL probe.
func MergeNative(items []Item, views []nodes.View, now time.Time) []Item {
	indices := map[string]int{}
	for i, item := range items {
		indices[item.NodeID] = i
	}
	for _, view := range views {
		var host agentv1.HostSnapshot
		raw, hasHost := view.Resources["host"]
		if hasHost {
			data, err := json.Marshal(raw)
			hasHost = err == nil && json.Unmarshal(data, &host) == nil && raw != nil
		}
		i, exists := indices[view.ID]
		if !hasHost && exists && items[i].LinkStatus == "linked" {
			continue
		}
		alive := view.LastHeartbeatAt != nil && now.Sub(*view.LastHeartbeatAt) <= 90*time.Second && now.Sub(*view.LastHeartbeatAt) >= -5*time.Second
		item := Item{NodeID: view.ID, LinkStatus: "native", Source: "hl", Online: &alive,
			Name: view.Name, HostPlatform: view.Platform, HostArchitecture: view.Architecture,
			AgentVersion: view.AgentVersion, RegisteredAt: &view.CreatedAt, SampledAt: view.LastHeartbeatAt}
		if view.NezhaServerID > 0 {
			item.NezhaServerID = &view.NezhaServerID
		}
		if hasHost {
			item.CPUPercent = host.CPUPercent
			item.MemoryUsedBytes, item.MemoryTotalBytes = host.MemoryUsedBytes, host.MemoryTotalBytes
			item.DiskUsedBytes, item.DiskTotalBytes = host.DiskUsedBytes, host.DiskTotalBytes
			item.UptimeSeconds = host.UptimeSeconds
			item.NetInTransferBytes, item.NetOutTransferBytes = host.NetInTransferBytes, host.NetOutTransferBytes
			item.NetInSpeedBytesPerSecond, item.NetOutSpeedBytesPerSecond = host.NetInSpeedBytesPerSecond, host.NetOutSpeedBytesPerSecond
			item.TCPConnCount, item.UDPConnCount = host.TCPConnCount, host.UDPConnCount
			item.IPv4, item.IPv6 = host.IPv4, host.IPv6
			if host.UptimeSeconds != nil && view.LastHeartbeatAt != nil && *host.UptimeSeconds <= uint64(view.LastHeartbeatAt.Unix()) {
				boot := uint64(view.LastHeartbeatAt.Unix()) - *host.UptimeSeconds
				item.HostBootTime = &boot
			}
		}
		item.IPv4, item.IPv6 = hostgeo.PublicIP(item.IPv4), hostgeo.PublicIP(item.IPv6)
		observed, _ := view.Resources["observed_ip"].(string)
		for _, address := range []string{observed, view.DialHost} {
			if ip := hostgeo.PublicIP(address); ip != "" {
				parsed, _ := netip.ParseAddr(ip)
				if parsed.Is4() && item.IPv4 == "" {
					item.IPv4 = ip
				} else if parsed.Is6() && item.IPv6 == "" {
					item.IPv6 = ip
				}
			}
		}
		if exists && ((item.IPv4 != "" && item.IPv4 == items[i].IPv4) || (item.IPv4 == "" && item.IPv6 != "" && item.IPv6 == items[i].IPv6)) {
			item.CountryCode = items[i].CountryCode
		}
		if exists {
			items[i] = item
		} else {
			items = append(items, item)
		}
	}
	return items
}
