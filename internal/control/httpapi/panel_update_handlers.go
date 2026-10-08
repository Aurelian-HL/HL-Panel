package httpapi

import (
	"net/http"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/panelupdate"
)

func WithPanelUpdate(service *panelupdate.Service) Option {
	return func(api *API) { api.panelUpdate = service }
}

func (api *API) panelUpdateStatus(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, api.panelUpdate.Status(r.Context()))
}

func (api *API) startPanelUpdate(w http.ResponseWriter, r *http.Request, session auth.Session) {
	var input struct {
		Version  string `json:"version"`
		Password string `json:"administrator_password"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeProblem(w, r, err)
		return
	}
	result, err := api.panelUpdate.Start(r.Context(), session.AdminID, input.Password, r.Header.Get("Idempotency-Key"), input.Version)
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusAccepted, result)
}
