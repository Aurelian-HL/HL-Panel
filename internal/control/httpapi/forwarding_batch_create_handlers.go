package httpapi

import (
	"net/http"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/forwarding"
)

func (api *API) batchCreateForwardingRules(w http.ResponseWriter, r *http.Request, session auth.Session) {
	var input forwarding.BatchCreateRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeProblem(w, r, err)
		return
	}
	result, replayed, err := api.forwarding.BatchCreateForAdministrator(r.Context(), session.AdminID, input, r.Header.Get("Idempotency-Key"))
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	writeBusinessResult(w, r, "result", result, replayed)
}
