package httpapi

import (
	"github.com/hongle/hl-panel/internal/control/auth"
	"net/http"
)

func (api *API) deleteNode(writer http.ResponseWriter, request *http.Request, session auth.Session) {
	result, replayed, err := api.nodes.DeleteOffline(request.Context(), session.AdminID, request.PathValue("node_id"), request.Header.Get("Idempotency-Key"))
	if err != nil {
		writeProblem(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, struct {
		NodeID   string `json:"node_id"`
		Replayed bool   `json:"replayed"`
	}{NodeID: result.NodeID, Replayed: replayed})
}
