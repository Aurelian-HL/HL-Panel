package httpapi

import (
	"net/http"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/diagnostics"
)

func (api *API) listDiagnosticTargets(w http.ResponseWriter, _ *http.Request, _ auth.Session) {
	writeJSON(w, http.StatusOK, map[string]any{"scope": "control-plane-local", "items": api.diagnostics.Targets()})
}

func (api *API) runDiagnostic(w http.ResponseWriter, r *http.Request, session auth.Session) {
	var input diagnostics.Request
	if err := decodeJSON(w, r, &input); err != nil {
		if auditErr := api.diagnostics.RecordRejected(r.Context(), session.AdminID); auditErr != nil {
			writeProblem(w, r, auditErr)
			return
		}
		writeProblem(w, r, err)
		return
	}
	result, err := api.diagnostics.Run(r.Context(), session.AdminID, input)
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": result})
}
