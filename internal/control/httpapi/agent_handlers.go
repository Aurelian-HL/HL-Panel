package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/generations"
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
	if ip := observedNodeIP(request); ip != "" {
		resources["observed_ip"] = ip
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

func (api *API) desiredControl(writer http.ResponseWriter, request *http.Request, node nodes.Node) {
	if node.ControlCommandID == "" || node.ControlCommandStatus != "pending" {
		writer.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(writer, http.StatusOK, agentv1.ControlCommandEnvelope{ID: node.ControlCommandID, Command: agentv1.ControlCommand(node.ControlCommand)})
}

func (api *API) recordControlResult(writer http.ResponseWriter, request *http.Request, node nodes.Node) {
	var input agentv1.ControlCommandResultRequest
	if err := decodeJSON(writer, request, &input); err != nil {
		writeProblem(writer, request, err)
		return
	}
	if input.ID == "" || (input.Status != "succeeded" && input.Status != "failed") || len(input.Message) > 2048 || len(input.Logs) > 16<<10 {
		writeProblem(writer, request, fmt.Errorf("%w: invalid control result", faults.ErrValidation))
		return
	}
	if err := api.nodes.RecordControlResult(request.Context(), node.ID, nodes.ControlCommandResult{NodeID: node.ID, CommandID: input.ID, Command: node.ControlCommand, Status: input.Status, Message: input.Message, Logs: input.Logs}); err != nil {
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
	writeDesiredConfig(writer, configuration)
}

func writeDesiredConfig(writer http.ResponseWriter, configuration generations.NodeConfigGeneration) {
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
