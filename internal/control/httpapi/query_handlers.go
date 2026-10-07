package httpapi

import (
	"net/http"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/control/panelruntime"
)

func (api *API) requestNodeControl(writer http.ResponseWriter, request *http.Request, session auth.Session) {
	var input struct {
		Command string `json:"command"`
	}
	if err := decodeJSON(writer, request, &input); err != nil {
		writeProblem(writer, request, err)
		return
	}
	result, replayed, err := api.nodes.RequestControl(request.Context(), session.AdminID, request.PathValue("node_id"), input.Command, request.Header.Get("Idempotency-Key"))
	if err != nil {
		writeProblem(writer, request, err)
		return
	}
	writeBusinessResult(writer, request, "control", result, replayed)
}

func (api *API) nodeControl(writer http.ResponseWriter, request *http.Request, _ auth.Session) {
	result, err := api.nodes.ControlForNode(request.Context(), request.PathValue("node_id"))
	if err != nil {
		writeProblem(writer, request, err)
		return
	}
	if result.CommandID == "" {
		writer.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (api *API) health(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]string{"status": "ok"})
}

func (api *API) overview(writer http.ResponseWriter, request *http.Request, _ auth.Session) {
	result, err := api.nodes.Overview(request.Context())
	if err != nil {
		writeProblem(writer, request, err)
		return
	}
	response := struct {
		nodes.Overview
		Panel *panelruntime.Runtime `json:"panel,omitempty"`
	}{Overview: result}
	if api.panelRuntime != nil {
		runtime := api.panelRuntime.Runtime(request.Context())
		response.Panel = &runtime
	}
	writeJSON(writer, http.StatusOK, response)
}

func (api *API) listNodes(writer http.ResponseWriter, request *http.Request, _ auth.Session) {
	items, err := api.nodes.List(request.Context())
	if err != nil {
		writeProblem(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, struct {
		Items []nodes.View `json:"items"`
	}{Items: items})
}

func (api *API) listDeviceGroups(writer http.ResponseWriter, request *http.Request, _ auth.Session) {
	items, err := api.groups.List(request.Context())
	if err != nil {
		writeProblem(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, struct {
		Items []groups.DeviceGroup `json:"items"`
	}{Items: items})
}
