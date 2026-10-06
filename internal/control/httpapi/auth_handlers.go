package httpapi

import (
	"net/http"
	"strings"

	"github.com/hongle/hl-panel/internal/control/auth"
)

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (api *API) currentAdministrator(writer http.ResponseWriter, request *http.Request, session auth.Session) {
	user, err := api.auth.CurrentAdministrator(request.Context(), session.AdminID)
	if err != nil {
		writeProblem(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, struct {
		User auth.AdministratorView `json:"user"`
	}{User: user})
}

type changeAdministratorPasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

func (api *API) changeAdministratorPassword(writer http.ResponseWriter, request *http.Request, session auth.Session) {
	var input changeAdministratorPasswordRequest
	if err := decodeJSON(writer, request, &input); err != nil {
		writeProblem(writer, request, err)
		return
	}
	if err := api.auth.ChangePassword(request.Context(), session.AdminID, input.CurrentPassword, input.NewPassword); err != nil {
		writeProblem(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (api *API) login(writer http.ResponseWriter, request *http.Request) {
	var input loginRequest
	if err := decodeJSON(writer, request, &input); err != nil {
		writeProblem(writer, request, err)
		return
	}
	result, err := api.auth.Login(request.Context(), strings.TrimSpace(input.Username), input.Password)
	if err != nil {
		writeProblem(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}
