package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/nodes"
)

// Same-process changes wake immediately; this timeout bounds cross-process
// recovery while keeping idle nodes from reconnecting too frequently.
const desiredWatchTimeout = 8 * time.Second

// Register before reading so a commit between the read and the wait cannot
// lose its notification. No database transaction is held while waiting.
func (api *API) watchDesiredNodeConfig(writer http.ResponseWriter, request *http.Request, node nodes.Node) {
	// Older agents do not send this hint. Their persisted heartbeat generation
	// still lets the server avoid replaying a configuration they already applied.
	appliedGeneration := node.AppliedGeneration
	if value := request.URL.Query().Get("applied_generation"); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed < 0 {
			writeProblem(writer, request, faults.ErrValidation)
			return
		}
		appliedGeneration = parsed
	}
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
	defer api.generations.ReleaseDesiredConfigChanges(node.ID, changed)
	configuration, err := api.generations.DesiredNodeConfig(request.Context(), node.ID)
	if err == nil && configuration.Generation > appliedGeneration {
		writeDesiredConfig(writer, configuration)
		return
	}
	if err != nil && !errors.Is(err, faults.ErrNotFound) {
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
	configuration, err = api.generations.DesiredNodeConfig(request.Context(), fresh.ID)
	if errors.Is(err, faults.ErrNotFound) || (err == nil && configuration.Generation <= appliedGeneration) {
		writer.Header().Set("X-Desired-Watch-Waited", "true")
		writer.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		writeProblem(writer, request, err)
		return
	}
	writeDesiredConfig(writer, configuration)
}
