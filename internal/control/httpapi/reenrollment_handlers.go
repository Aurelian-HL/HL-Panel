package httpapi

import (
	"net/http"

	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func (api *API) reenrollNode(writer http.ResponseWriter, request *http.Request) {
	node, err := api.nodes.AuthenticateReenrollmentCredential(request.Context(), bearerToken(request.Header.Get("Authorization")))
	if err != nil {
		writeProblem(writer, request, err)
		return
	}
	var input agentv1.EnrollmentRequest
	if err := decodeJSON(writer, request, &input); err != nil {
		writeProblem(writer, request, err)
		return
	}
	result, err := api.enrollment.Reenroll(request.Context(), node, enrollment.EnrollInput{
		RawToken: input.EnrollmentToken, Hostname: input.Hostname, DialHost: input.DialHost,
		Platform: input.Platform, Architecture: input.Architecture, AgentVersion: input.AgentVersion,
	})
	if err != nil {
		writeProblem(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, struct {
		NodeID string `json:"node_id"`
	}{NodeID: result.ID})
}
