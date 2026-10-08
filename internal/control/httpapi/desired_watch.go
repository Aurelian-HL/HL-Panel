package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/nodes"
)

const desiredWatchTimeout = 8 * time.Second

// Register before reading so a commit between the read and the wait cannot
// lose its notification. No database transaction is held while waiting.
func (api *API) watchDesiredNodeConfig(writer http.ResponseWriter, request *http.Request, node nodes.Node) {
	wait := desiredWatchTimeout
	if value := request.URL.Query().Get("wait_ms"); value != "" {
		milliseconds, err := strconv.Atoi(value)
		if err != nil || milliseconds < 1 || milliseconds > int(desiredWatchTimeout.Milliseconds()) {
			writeProblem(writer, request, faults.ErrValidation)
			return
		}
		wait = time.Duration(milliseconds) * time.Millisecond
	}
	changed := api.generations.DesiredConfigChanges(node.ID)
	configuration, err := api.generations.DesiredNodeConfig(request.Context(), node.ID)
	if err == nil {
		writeDesiredConfig(writer, configuration)
		return
	}
	if !errors.Is(err, faults.ErrNotFound) {
		writeProblem(writer, request, err)
		return
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-request.Context().Done():
		return
	case <-changed:
	case <-timer.C:
	}
	// A node can be deleted or reenrolled while the connection is waiting.
	// Reauthenticate before returning any private engine configuration.
	fresh, err := api.nodes.AuthenticateCredential(request.Context(), bearerToken(request.Header.Get("Authorization")))
	if err != nil {
		writer.Header().Set("WWW-Authenticate", "Bearer")
		writeProblem(writer, request, err)
		return
	}
	api.desiredNodeConfig(writer, request, fresh)
}
