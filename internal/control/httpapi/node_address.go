package httpapi

import (
	"net"
	"net/http"
	"net/netip"

	"github.com/hongle/hl-panel/internal/control/hostgeo"
)

func observedNodeIP(request *http.Request) string {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		return ""
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return ""
	}
	// The shipped loopback Nginx proxy overwrites X-Real-IP. Remote peers must
	// never be allowed to select their observed address through proxy headers.
	if peer.IsLoopback() {
		return hostgeo.PublicIP(request.Header.Get("X-Real-IP"))
	}
	return hostgeo.PublicIP(host)
}
