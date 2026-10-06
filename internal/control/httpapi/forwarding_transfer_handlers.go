package httpapi

import (
	"fmt"
	"net/http"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/forwarding"
)

func (api *API) previewForwardingImport(w http.ResponseWriter, r *http.Request, session auth.Session) {
	var input forwarding.TransferRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeProblem(w, r, err)
		return
	}
	preview, err := api.forwarding.PreviewImportForAdministrator(r.Context(), session.AdminID, input)
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"preview": preview})
}

func (api *API) importForwardingRules(w http.ResponseWriter, r *http.Request, session auth.Session) {
	var input forwarding.TransferRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeProblem(w, r, err)
		return
	}
	result, replayed, err := api.forwarding.ImportForAdministrator(r.Context(), session.AdminID, input, r.Header.Get("Idempotency-Key"))
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	writeBusinessResult(w, r, "result", result, replayed)
}

func (api *API) exportForwardingRules(w http.ResponseWriter, r *http.Request, session auth.Session) {
	document, err := api.forwarding.ExportForAdministrator(r.Context(), session.AdminID)
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="forwarding-rules-%s.json"`, document.ExportedAt.Format("20060102-150405")))
	writeJSON(w, http.StatusOK, document)
}
