package hostprobe

import (
	"net"

	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func collectIPAddresses(snapshot *agentv1.HostSnapshot, addresses []net.Addr, err error) {
	if err != nil {
		snapshot.IPCollectionStatus = "unavailable"
		return
	}
	snapshot.IPCollectionStatus = "collected"
	for _, address := range addresses {
		ip, _, err := net.ParseCIDR(address.String())
		if err != nil || !ip.IsGlobalUnicast() || ip.IsPrivate() {
			continue
		}
		if ip.To4() != nil && snapshot.IPv4 == "" {
			snapshot.IPv4 = ip.String()
		} else if ip.To4() == nil && snapshot.IPv6 == "" {
			snapshot.IPv6 = ip.String()
		}
	}
}
