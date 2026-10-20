package httpapi

import (
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/vlessconnection"
	"net/http"
)

type forwardingConnectionResponse struct {
	URI      string `json:"uri"`
	Name     string `json:"name"`
	Endpoint string `json:"endpoint"`
	Status   string `json:"status"`
}

// getForwardingRuleConnection exposes the authorized client URI projection.
// It never returns server-side credentials or Reality private material.
func (api *API) getForwardingRuleConnection(w http.ResponseWriter, r *http.Request, session auth.Session) {
	connection, err := api.vlessConnectionService().ResolveForwardingConnection(r.Context(), session.AdminID, r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, connection)
}

func (api *API) getVLESSIdentityConnection(w http.ResponseWriter, r *http.Request, session auth.Session) {
	connection, err := api.vlessConnectionService().ResolveIdentityConnection(r.Context(), session.AdminID, r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, connection)
}

func (api *API) vlessConnectionService() *vlessconnection.Service {
	if api.vlessConnections != nil {
		return api.vlessConnections
	}
	return vlessconnection.NewService(api.vlessIdentity, api.forwarding, api.endpoints)
}
