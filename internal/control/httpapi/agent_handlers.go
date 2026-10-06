package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func (api *API) heartbeat(writer http.ResponseWriter, request *http.Request, node nodes.Node) {
	var input agentv1.HeartbeatRequest
	if err := decodeJSON(writer, request, &input); err != nil {
		writeProblem(writer, request, err)
		return
	}
	resources := map[string]any{
		"logical_cpus":        input.Resources.LogicalCPUs,
		"go_max_procs":        input.Resources.GoMaxProcs,
		"memory_alloc_bytes":  input.Resources.MemoryAllocBytes,
		"memory_system_bytes": input.Resources.MemorySystemBytes,
	}
	if input.Resources.Host != nil {
		resources["host"] = input.Resources.Host
	}
	_, err := api.nodes.RecordHeartbeat(request.Context(), node.ID, nodes.Heartbeat{
		Hostname:                 input.Hostname,
		Platform:                 input.Platform,
		Architecture:             input.Architecture,
		BootID:                   input.BootID,
		AgentVersion:             input.AgentVersion,
		EngineVersions:           input.EngineVersions,
		Resources:                resources,
		Capabilities:             input.Capabilities,
		CurrentAppliedGeneration: int64(input.AppliedGeneration),
		LastApplyStatus:          string(input.LastApplyStatus),
	})
	if err != nil {
		writeProblem(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (api *API) desiredNodeConfig(writer http.ResponseWriter, request *http.Request, node nodes.Node) {
	configuration, err := api.generations.DesiredNodeConfig(request.Context(), node.ID)
	if errors.Is(err, faults.ErrNotFound) {
		writer.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		writeProblem(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, agentv1.DesiredNodeConfig{
		Generation:   agentv1.NodeConfigGeneration(configuration.Generation),
		Engine:       configuration.Engine,
		ConfigSHA256: configuration.ConfigSHA256,
		Config:       json.RawMessage(configuration.Config),
	})
}

func (api *API) recordApplyResult(writer http.ResponseWriter, request *http.Request, node nodes.Node) {
	var input agentv1.ApplyResultRequest
	if err := decodeJSON(writer, request, &input); err != nil {
		writeProblem(writer, request, err)
		return
	}
	_, err := api.generations.RecordApplyResult(
		request.Context(),
		node.ID,
		int64(input.Generation),
		input.Phase,
		input.Status,
		input.ConfigSHA256,
		input.EngineMode,
		input.Message,
		input.AttemptID,
	)
	if err != nil {
		writeProblem(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}
