package httpapi

import (
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/vlessconnection"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
	"net/http"
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
	record, err := api.vlessIdentity.CredentialForAdministrator(r.Context(), session.AdminID, rule.ID)
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	api.writeVLESSConnection(w, r, session.AdminID, record)
}

func (api *API) getVLESSIdentityConnection(w http.ResponseWriter, r *http.Request, session auth.Session) {
	record, err := api.vlessIdentity.CredentialByIDForAdministrator(r.Context(), session.AdminID, r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	api.writeVLESSConnection(w, r, session.AdminID, record)
}

func (api *API) writeVLESSConnection(w http.ResponseWriter, r *http.Request, administratorID string, record vlessidentity.CredentialRecord) {
	connection, err := vlessconnection.NewService(api.vlessIdentity, api.forwarding, api.endpoints).Resolve(r.Context(), administratorID, record)
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, connection)
}
