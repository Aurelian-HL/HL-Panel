package memoryrepo

import (
	"encoding/json"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/nodes"
)

func cloneAdministrator(admin auth.Administrator) auth.Administrator {
	admin.PasswordHash = append([]byte(nil), admin.PasswordHash...)
	return admin
}

func cloneNode(node nodes.Node) nodes.Node {
	if node.DeletedAt != nil {
		node.DeletedAt = timePointer(*node.DeletedAt)
	}
	node.Capabilities = cloneStrings(node.Capabilities)
	node.EngineVersions = cloneStringMap(node.EngineVersions)
	node.Resources = cloneAnyMap(node.Resources)
	if node.LastHeartbeatAt != nil {
		node.LastHeartbeatAt = timePointer(*node.LastHeartbeatAt)
	}
	return node
}

func cloneStringMap(input map[string]string) map[string]string {
	if input == nil {
		return map[string]string{}
	}
	result := make(map[string]string, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func cloneAnyMap(input map[string]any) map[string]any {
	if input == nil {
		return map[string]any{}
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return map[string]any{}
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		return map[string]any{}
	}
	return result
}

func cloneStrings(input []string) []string {
	if input == nil {
		return []string{}
	}
	return append([]string(nil), input...)
}

func cloneRaw(input json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), input...)
}

func cloneGroupRevision(revision generations.GroupRevision) generations.GroupRevision {
	revision.Config = cloneRaw(revision.Config)
	return revision
}

func cloneNodeConfig(configuration generations.NodeConfigGeneration) generations.NodeConfigGeneration {
	configuration.Config = cloneRaw(configuration.Config)
	return configuration
}

func cloneNodeConfigs(configurations []generations.NodeConfigGeneration) []generations.NodeConfigGeneration {
	result := make([]generations.NodeConfigGeneration, len(configurations))
	for index := range configurations {
		result[index] = cloneNodeConfig(configurations[index])
	}
	return result
}

func cloneAuditEvent(event audit.Event) audit.Event {
	event.Metadata = cloneAnyMap(event.Metadata)
	return event
}

func timePointer(value time.Time) *time.Time {
	copy := value
	return &copy
}
