package httpapi

import (
	"net/http"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/customers"
)

func (api *API) deleteServiceOwner(w http.ResponseWriter, r *http.Request, session auth.Session) {
	var input customers.DeleteRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeProblem(w, r, err)
		return
	}
	replayed, err := api.customers.DeleteServiceOwner(r.Context(), session.AdminID, r.PathValue("id"), input, r.Header.Get("Idempotency-Key"))
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"replayed": replayed})
}
