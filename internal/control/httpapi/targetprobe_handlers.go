package httpapi

import (
	"net/http"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/targetprobe"
)

func (api *API) probeForwardingTarget(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	var input targetprobe.Request
	if err := decodeJSON(w, r, &input); err != nil {
		writeProblem(w, r, err)
		return
	}
	result, err := api.targetProbe.Probe(r.Context(), input)
	if err != nil {
		writeProblem(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"probe": result})
}
