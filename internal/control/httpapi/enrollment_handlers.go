package httpapi

import (
	"net/http"
	"time"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

type issueEnrollmentTokenRequest struct {
	Name             string `json:"name"`
	GroupID          string `json:"group_id,omitempty"`
	NezhaServerID    uint64 `json:"nezha_server_id,omitempty"`
	ExpiresInSeconds int64  `json:"expires_in_seconds"`
}

func (api *API) issueEnrollmentToken(writer http.ResponseWriter, request *http.Request, session auth.Session) {
	var input issueEnrollmentTokenRequest
	if err := decodeJSON(writer, request, &input); err != nil {
		writeProblem(writer, request, err)
		return
	}
	result, err := api.enrollment.IssueForGroupWithNezha(request.Context(), session.AdminID, input.Name, input.GroupID, input.NezhaServerID, time.Duration(input.ExpiresInSeconds)*time.Second)
	if err != nil {
		writeProblem(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusCreated, result)
}

func (api *API) revokeEnrollmentToken(writer http.ResponseWriter, request *http.Request, session auth.Session) {
	result, replayed, err := api.enrollment.Revoke(request.Context(), session.AdminID, request.PathValue("token_id"), request.Header.Get("Idempotency-Key"))
	if err != nil {
		writeProblem(writer, request, err)
		return
	}
	writeBusinessResult(writer, request, "token", result, replayed)
}

func (api *API) listPendingGroupEnrollmentTokens(writer http.ResponseWriter, request *http.Request, _ auth.Session) {
	items, err := api.enrollment.ListPendingForGroup(request.Context(), request.PathValue("group_id"))
	if err != nil {
		writeProblem(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, struct {
		Items []enrollment.PendingToken `json:"items"`
	}{Items: items})
}

func (api *API) enrollNode(writer http.ResponseWriter, request *http.Request) {
	var input agentv1.EnrollmentRequest
	if err := decodeJSON(writer, request, &input); err != nil {
		writeProblem(writer, request, err)
		return
	}
	result, err := api.enrollment.Enroll(request.Context(), enrollment.EnrollInput{
		RawToken:     input.EnrollmentToken,
		Hostname:     input.Hostname,
		Platform:     input.Platform,
		Architecture: input.Architecture,
		AgentVersion: input.AgentVersion,
		Capabilities: input.Capabilities,
	})
	if err != nil {
		writeProblem(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusCreated, agentv1.EnrollmentResponse{
		NodeID:         result.NodeID,
		NodeCredential: result.NodeCredential,
	})
}
