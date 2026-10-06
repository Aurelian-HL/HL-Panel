package customerapi

import (
	"net/http"
	"strings"

	"github.com/hongle/hl-panel/internal/control/customeridentity"
)

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

func (api *API) login(writer http.ResponseWriter, request *http.Request) {
	var input loginRequest
	if err := decodeJSON(writer, request, &input); err != nil {
		writeProblem(writer, err)
		return
	}
	result, err := api.identity.Login(request.Context(), strings.TrimSpace(input.Username), input.Password)
	if err != nil {
		writeProblem(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (api *API) logout(writer http.ResponseWriter, request *http.Request, principal customeridentity.Principal) {
	if err := api.identity.Logout(request.Context(), principal); err != nil {
		writeProblem(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (api *API) portal(writer http.ResponseWriter, request *http.Request) {
	view, err := api.identity.Portal(request.Context())
	if err != nil {
		writeProblem(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"portal": view})
}

func (api *API) me(writer http.ResponseWriter, request *http.Request, principal customeridentity.Principal) {
	view, err := api.identity.Me(request.Context(), principal)
	if err != nil {
		writeProblem(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"user": view})
}

func (api *API) changePassword(writer http.ResponseWriter, request *http.Request, principal customeridentity.Principal) {
	var input changePasswordRequest
	if err := decodeJSON(writer, request, &input); err != nil {
		writeProblem(writer, err)
		return
	}
	replayed, err := api.identity.ChangePassword(request.Context(), principal, input.CurrentPassword, input.NewPassword, request.Header.Get("Idempotency-Key"))
	if err != nil {
		writeProblem(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]bool{"replayed": replayed})
}

func (api *API) rules(writer http.ResponseWriter, request *http.Request, principal customeridentity.Principal) {
	items, err := api.identity.Rules(request.Context(), principal)
	if err != nil {
		writeProblem(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items})
}

func (api *API) usage(writer http.ResponseWriter, request *http.Request, principal customeridentity.Principal) {
	view, err := api.identity.Usage(request.Context(), principal)
	if err != nil {
		writeProblem(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"usage": view})
}
