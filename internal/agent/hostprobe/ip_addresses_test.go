package hostprobe

import (
	"errors"
	"net"
	"testing"

	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func TestIPCollectionDistinguishesMissingFamilyFromFailure(t *testing.T) {
	var addresses []net.Addr
	for _, cidr := range []string{"127.0.0.1/8", "10.0.0.2/24", "fe80::1/64", "fd00::1/64", "203.0.113.8/24"} {
		ip, network, err := net.ParseCIDR(cidr)
		if err != nil {
			t.Fatal(err)
		}
		network.IP = ip
		addresses = append(addresses, network)
	}
	snapshot := &agentv1.HostSnapshot{}
	collectIPAddresses(snapshot, addresses, nil)
	if snapshot.IPCollectionStatus != "collected" || snapshot.IPv4 != "203.0.113.8" || snapshot.IPv6 != "" {
		t.Fatalf("missing IPv6 is not a collection failure: %+v", snapshot)
	}
	failed := &agentv1.HostSnapshot{}
	collectIPAddresses(failed, nil, errors.New("netlink socket denied"))
	if failed.IPCollectionStatus != "unavailable" || failed.IPv4 != "" || failed.IPv6 != "" {
		t.Fatalf("permission failure was hidden: %+v", failed)
	}
}

func TestIPCollectionSupportsIPv6Only(t *testing.T) {
	ip, network, _ := net.ParseCIDR("2001:db8::8/64")
	network.IP = ip
	snapshot := &agentv1.HostSnapshot{}
	collectIPAddresses(snapshot, []net.Addr{network}, nil)
	if snapshot.IPCollectionStatus != "collected" || snapshot.IPv4 != "" || snapshot.IPv6 != "2001:db8::8" {
		t.Fatalf("IPv6-only address lost: %+v", snapshot)
	}
}
