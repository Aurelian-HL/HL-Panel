package httpapi

import (
	"net/http"

	"github.com/hongle/hl-panel/internal/control/auth"
)

func (api *API) getForwardingRuleConfiguration(w http.ResponseWriter, r *http.Request, session auth.Session) {
	subscriptionHeaders(w)
	configuration, err := api.vlessConnectionService().ResolveForwardingConfiguration(r.Context(), session.AdminID, r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, configuration)
}
