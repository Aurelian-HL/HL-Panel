package engine

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

const unknownEngineVersion = "unknown"
const maxEngineVersionBytes = 4096

// The release updater admits this exact Xray core after VLESS Reality and
// DIRECT UDP compatibility testing. Other versions need their own acceptance.
const verifiedVLESSUDPDirectXrayVersion = "26.7.28"

var engineVersionNumber = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(?:-[a-zA-Z0-9]+(?:[-.][a-zA-Z0-9]+)*)?$`)

// Read only the executable's fixed version command during runner initialization.
// Heartbeats use the cached number and never spawn another process. Failure is
// reported as unknown, not as a guessed pinned or configured version.
func readEngineVersion(binaryPath, family, argument string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binaryPath, argument)
	output := &engineVersionOutput{}
	command.Stdout, command.Stderr = output, io.Discard
	if command.Run() != nil {
		return unknownEngineVersion
	}
	return parseEngineVersion(family, output.Bytes())
}

// Do not forward arbitrary version-command output to telemetry or logs.
func parseEngineVersion(family string, output []byte) string {
	if len(output) == 0 || len(output) > maxEngineVersionBytes {
		return unknownEngineVersion
	}
	firstLine, _, _ := strings.Cut(strings.TrimSpace(string(output)), "\n")
	fields := strings.Fields(firstLine)
	if len(fields) < 2 || !strings.EqualFold(fields[0], family) || !engineVersionNumber.MatchString(fields[1]) {
		return unknownEngineVersion
	}
	return strings.TrimPrefix(fields[1], "v")
}

type engineVersionOutput struct{ bytes.Buffer }

func (b *engineVersionOutput) Write(data []byte) (int, error) {
	if len(data) > maxEngineVersionBytes-b.Len() {
		return 0, errors.New("engine version output exceeds limit")
	}
	return b.Buffer.Write(data)
}

func (r *ExecXrayRunner) EngineVersion() string { return reportedEngineVersion(r.version) }
func (r *ExecGOSTRunner) EngineVersion() string { return reportedEngineVersion(r.version) }

func reportedEngineVersion(version string) string {
	if version == "" {
		return unknownEngineVersion
	}
	return version
}

// An injected runner without this capability cannot attest a binary version.
func (a *XrayProcessAdapter) EngineVersion() string {
	if provider, ok := a.runner.(interface{ EngineVersion() string }); ok {
		return provider.EngineVersion()
	}
	return unknownEngineVersion
}

func (a *XrayProcessAdapter) SupportsVLESSUDPDirect() bool {
	return a != nil && a.autoStart && a.EngineVersion() == verifiedVLESSUDPDirectXrayVersion
}

func (a *GOSTProcessAdapter) EngineVersion() string {
	if provider, ok := a.runner.(interface{ EngineVersion() string }); ok {
		return provider.EngineVersion()
	}
	return unknownEngineVersion
}
