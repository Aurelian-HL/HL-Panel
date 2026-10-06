package httpapi

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	provisioningvless "github.com/hongle/hl-panel/internal/provisioning/vless"
)

type forwardingConnectionResponse struct {
	URI      string `json:"uri"`
	Name     string `json:"name"`
	Endpoint string `json:"endpoint"`
	Status   string `json:"status"`
}

// getForwardingRuleConnection exposes the same customer-safe URI projection
// to the rule owner. It never returns UUIDs or Reality private material.
func (api *API) getForwardingRuleConnection(w http.ResponseWriter, r *http.Request, session auth.Session) {
	rule, err := api.forwarding.GetForAdministrator(r.Context(), session.AdminID, r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	if rule.EffectiveIngressProtocol() != forwarding.IngressVLESSReality || rule.Protocol != forwarding.ProtocolTCP || rule.Paused || !rule.Deployed || rule.Status != forwarding.StatusActive || rule.VLESSFlow != "xtls-rprx-vision" || rule.RealityServerName == "" || rule.RealityPublicKey == "" || rule.RealityShortID == "" || rule.RealityDestination == "" {
		writeProblem(w, r, faults.ErrConflict)
		return
	}
	pools, err := api.endpoints.ListPoolsForAdministrator(r.Context(), session.AdminID)
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	var pool endpoints.EndpointPool
	for _, candidate := range pools {
		if candidate.RuleID == rule.ID && candidate.GroupID == rule.EntryGroupID && candidate.Protocol == "vless" {
			pool = candidate
			break
		}
	}
	if pool.ID == "" {
		writeProblem(w, r, faults.ErrNotFound)
		return
	}
	members, err := api.endpoints.CandidateSet(r.Context(), pool.ID, time.Now().UTC(), endpoints.DefaultHealthTTL)
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	if len(members) == 0 {
		writeProblem(w, r, faults.ErrConflict)
		return
	}
	record, err := api.vlessIdentity.CredentialForAdministrator(r.Context(), session.AdminID, rule.ID)
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	uri, err := provisioningvless.URI(provisioningvless.Profile{
		Endpoint: provisioningvless.Endpoint{Hostname: pool.Hostname, Port: pool.Port, Name: pool.Name},
		Identity: provisioningvless.Identity{UUID: record.CredentialUUID, Flow: rule.VLESSFlow},
		Reality:  &provisioningvless.RealityProfile{ServerName: rule.RealityServerName, PublicKey: rule.RealityPublicKey, ShortID: rule.RealityShortID, Destination: rule.RealityDestination, Fingerprint: "chrome"},
	})
	if err != nil || !strings.HasPrefix(uri, "vless://") {
		writeProblem(w, r, faults.ErrConflict)
		return
	}
	writeJSON(w, http.StatusOK, forwardingConnectionResponse{URI: uri, Name: rule.Name, Endpoint: net.JoinHostPort(pool.Hostname, strconv.Itoa(pool.Port)), Status: "ready"})
}
