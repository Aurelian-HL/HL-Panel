package httpapi

import (
	"net/http"

	"github.com/hongle/hl-panel/internal/control/auth"
)

type rotateNodeCredentialRequest struct{}

func (api *API) rotateNodeCredential(writer http.ResponseWriter, request *http.Request, session auth.Session) {
	var input rotateNodeCredentialRequest
	if err := decodeJSON(writer, request, &input); err != nil {
		writeProblem(writer, request, err)
		return
	}
	result, replayed, err := api.nodes.RotateCredential(request.Context(), session.AdminID, request.PathValue("node_id"), request.Header.Get("Idempotency-Key"))
	if err != nil {
		writeProblem(writer, request, err)
		return
	}
	writeBusinessResult(writer, request, "credential", result, replayed)
}
