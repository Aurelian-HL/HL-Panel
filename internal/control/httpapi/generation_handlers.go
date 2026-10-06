package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

type createGroupRevisionRequest struct {
	Engine         agentv1.Engine  `json:"engine"`
	Config         json.RawMessage `json:"config"`
	IdempotencyKey string          `json:"idempotency_key"`
}

func (api *API) createGroupRevision(writer http.ResponseWriter, request *http.Request, session auth.Session) {
	var input createGroupRevisionRequest
	if err := decodeJSON(writer, request, &input); err != nil {
		writeProblem(writer, request, err)
		return
	}
	result, err := api.generations.CreateGroupRevision(request.Context(), session.AdminID, request.PathValue("group_id"), input.Engine, input.Config, input.IdempotencyKey)
	if err != nil {
		writeProblem(writer, request, err)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	writeJSON(writer, status, result)
}
