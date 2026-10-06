package httpapi

import (
	"net/http"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/nodes"
)

func (api *API) health(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]string{"status": "ok"})
}

func (api *API) overview(writer http.ResponseWriter, request *http.Request, _ auth.Session) {
	result, err := api.nodes.Overview(request.Context())
	if err != nil {
		writeProblem(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
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
