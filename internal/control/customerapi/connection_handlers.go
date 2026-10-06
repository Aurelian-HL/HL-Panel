package customerapi

import (
	"net/http"

	"github.com/hongle/hl-panel/internal/control/customeridentity"
)

func (api *API) connectionsView(writer http.ResponseWriter, request *http.Request, principal customeridentity.Principal) {
	items, err := api.connections.List(request.Context(), principal)
	if err != nil {
		writeProblem(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items})
}
