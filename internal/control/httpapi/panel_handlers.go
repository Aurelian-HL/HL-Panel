package httpapi

import (
	"net/http"

	"github.com/hongle/hl-panel/internal/control/auth"
)

func (api *API) panelRuntimeStatus(writer http.ResponseWriter, request *http.Request, _ auth.Session) {
	writeJSON(writer, http.StatusOK, api.panelRuntime.Runtime(request.Context()))
}

func (api *API) requestPanelControl(writer http.ResponseWriter, request *http.Request, _ auth.Session) {
	var input struct {
		Command string `json:"command"`
	}
	if err := decodeJSON(writer, request, &input); err != nil {
		writeProblem(writer, request, err)
		return
	}
	result := api.panelRuntime.Control(request.Context(), input.Command)
	status := http.StatusOK
	if result.Status == "failed" {
		status = http.StatusServiceUnavailable
	}
	writeJSON(writer, status, result)
}

func (api *API) panelControl(writer http.ResponseWriter, request *http.Request, _ auth.Session) {
	result, ok := api.panelRuntime.LastControl(request.Context())
	if !ok {
		writer.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}
