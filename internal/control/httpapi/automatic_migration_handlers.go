package httpapi

import (
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/panelmigration"
	"net/http"
)

func (api *API) automaticMigrationStatus(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, api.automaticMigration.Status())
}
func (api *API) startAutomaticMigration(w http.ResponseWriter, r *http.Request, session auth.Session) {
	api.changeAutomaticMigration(w, r, session, false)
}
func (api *API) rollbackAutomaticMigration(w http.ResponseWriter, r *http.Request, session auth.Session) {
	api.changeAutomaticMigration(w, r, session, true)
}
func (api *API) changeAutomaticMigration(w http.ResponseWriter, r *http.Request, session auth.Session, rollback bool) {
	var input panelmigration.Input
	if err := decodeJSON(w, r, &input); err != nil {
		writeProblem(w, r, err)
		return
	}
	var result panelmigration.Status
	var err error
	if rollback {
		result, err = api.automaticMigration.Rollback(r.Context(), session.AdminID, input)
	} else {
		result, err = api.automaticMigration.Start(r.Context(), session.AdminID, input)
	}
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusAccepted, result)
}
func (api *API) probeAutomaticMigration(w http.ResponseWriter, r *http.Request, session auth.Session) {
	var input struct {
		Target   panelmigration.Target `json:"target"`
		Password string                `json:"administrator_password"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeProblem(w, r, err)
		return
	}
	fingerprint, err := api.automaticMigration.Probe(r.Context(), session.AdminID, input.Password, input.Target)
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]string{"fingerprint": fingerprint})
}
func (api *API) continueAutomaticMigration(w http.ResponseWriter, r *http.Request, session auth.Session) {
	var input struct {
		ID       string `json:"id"`
		Password string `json:"administrator_password"`
		Confirm  string `json:"confirm"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeProblem(w, r, err)
		return
	}
	result, err := api.automaticMigration.Continue(r.Context(), session.AdminID, input.Password, input.ID, input.Confirm)
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusAccepted, result)
}
