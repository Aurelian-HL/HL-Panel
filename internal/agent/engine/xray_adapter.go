package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

var (
	// ErrXrayUnsupportedFragment means that a bundle contains a fragment which
	// this adapter cannot safely execute. In particular, GOST is never silently
	// translated into an Xray configuration.
	ErrXrayUnsupportedFragment = errors.New("xray adapter received an unsupported fragment engine")
	ErrXrayConfigConflict      = errors.New("xray fragments contain conflicting configuration")
	ErrXrayValidation          = errors.New("xray configuration validation failed")
	ErrXrayStart               = errors.New("xray process could not be started")
	ErrXrayNotRunning          = errors.New("xray process is not running")
)

// XrayProcess is the small lifecycle surface needed by the adapter. Keeping
// this interface local makes process management testable without starting a
// real engine, while the production implementation still uses a fixed binary
// and argv (never a shell).
type XrayProcess interface {
	Stop(context.Context) error
	Running() bool
}

// XrayRunner validates and starts one local Xray process. Implementations must
// not disclose command output because a configuration can contain credentials.
type XrayRunner interface {
	Test(context.Context, string) error
	Start(context.Context, string) (XrayProcess, error)
}

// XrayAdapterOptions controls the isolated filesystem and optional process
// lifecycle used by XrayProcessAdapter. A nil Runner is replaced with the
// fixed absolute BinaryPath runner. AutoStart must be explicitly enabled by a
// caller that intends to manage a real process.
type XrayAdapterOptions struct {
	StagingDir string
	ActivePath string
	BinaryPath string
	Runner     XrayRunner
	AutoStart  bool
}

// XrayProcessAdapter validates an Xray-only node bundle, compiles its
// fragments into one native Xray document, atomically activates that document,
// and optionally owns one local Xray process. It is deliberately separate from
// DryRunFileAdapter so the existing runtime remains dry-run until an explicit
// integration configuration selects this adapter.
type XrayProcessAdapter struct {
	stagingDir string
	activePath string
	binaryPath string
	runner     XrayRunner
	autoStart  bool

	counterEpoch uint64
	mu           sync.Mutex
	process      XrayProcess
}

// NewXrayProcessAdapter constructs an adapter rooted at private absolute
// paths. A caller supplying a fake/test Runner may omit BinaryPath; production
// callers must provide an absolute, regular binary path.
func NewXrayProcessAdapter(options XrayAdapterOptions) (*XrayProcessAdapter, error) {
	stagingDir := filepath.Clean(strings.TrimSpace(options.StagingDir))
	activePath := filepath.Clean(strings.TrimSpace(options.ActivePath))
	if stagingDir == "." || !filepath.IsAbs(stagingDir) {
		return nil, errors.New("xray staging directory must be an absolute path")
	}
	if activePath == "." || !filepath.IsAbs(activePath) {
		return nil, errors.New("xray active path must be an absolute path")
	}
	if filepath.Clean(activePath) == stagingDir {
		return nil, errors.New("xray active path must be a file separate from staging directory")
	}
	// Keeping the active file outside the staging directory prevents an
	// accidental recursive cleanup from removing the last-known-good config.
	stagingPrefix := stagingDir + string(filepath.Separator)
	if strings.HasPrefix(activePath, stagingPrefix) {
		return nil, errors.New("xray active path must not be inside staging directory")
	}

	binaryPath := filepath.Clean(strings.TrimSpace(options.BinaryPath))
	runner := options.Runner
	if runner == nil {
		if binaryPath == "." || !filepath.IsAbs(binaryPath) {
			return nil, errors.New("xray binary path must be an absolute path")
		}
		fixed, err := NewExecXrayRunner(binaryPath)
		if err != nil {
			return nil, err
		}
		runner = fixed
	}
	return &XrayProcessAdapter{
		stagingDir: stagingDir,
		activePath: activePath,
		binaryPath: binaryPath,
		runner:     runner,
		autoStart:  options.AutoStart,
	}, nil
}

func (a *XrayProcessAdapter) Prepare(ctx context.Context, desired agentv1.DesiredNodeConfig) (PreparedConfiguration, error) {
	if err := ctx.Err(); err != nil {
		return PreparedConfiguration{}, err
	}
	prepared, err := parseDesired(desired)
	if err != nil {
		return PreparedConfiguration{}, err
	}
	native, err := compileXrayBundle(prepared.Bundle)
	if err != nil {
		return PreparedConfiguration{}, err
	}
	if err := ensurePrivateDir(a.stagingDir); err != nil {
		return PreparedConfiguration{}, fmt.Errorf("prepare xray staging directory: %w", err)
	}
	// The filename contains only a generation and a hash prefix. It never
	// contains a customer identity, UUID, or private key.
	stagedPath := filepath.Join(a.stagingDir, fmt.Sprintf("node-bundle-%d-%s.json", prepared.Generation, prepared.ConfigSHA256[:16]))
	if err := writeFileAtomic(stagedPath, native, 0o600); err != nil {
		return PreparedConfiguration{}, fmt.Errorf("stage xray configuration: %w", err)
	}
	prepared.adapterPath = stagedPath
	return prepared, nil
}

func (a *XrayProcessAdapter) Validate(ctx context.Context, prepared PreparedConfiguration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := a.checkOwner(prepared); err != nil {
		return err
	}
	if err := a.verifyStaged(prepared); err != nil {
		return fmt.Errorf("validate staged xray configuration: %w", err)
	}
	if err := a.runner.Test(ctx, prepared.adapterPath); err != nil {
		// Deliberately discard runner output. Xray diagnostics can contain the
		// full configuration, including UUIDs and Reality private keys.
		return fmt.Errorf("%w", ErrXrayValidation)
	}
	return nil
}

func (a *XrayProcessAdapter) Commit(ctx context.Context, prepared PreparedConfiguration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.Validate(ctx, prepared); err != nil {
		return err
	}
	candidate, err := readRegularFile(prepared.adapterPath, MaxConfigurationBytes)
	if err != nil {
		return fmt.Errorf("read staged xray configuration: %w", err)
	}
	previous, previousExists, err := readOptionalRegularFile(a.activePath)
	if err != nil {
		return fmt.Errorf("read active xray configuration: %w", err)
	}
	oldProcess := a.process
	oldRunning := oldProcess != nil && oldProcess.Running()
	if a.autoStart && oldRunning {
		if err := oldProcess.Stop(context.WithoutCancel(ctx)); err != nil {
			return fmt.Errorf("stop previous xray process: %w", err)
		}
		a.process = nil
	}
	if err := writeFileAtomic(a.activePath, candidate, 0o600); err != nil {
		if a.autoStart && oldRunning {
			_ = a.restartPrevious(context.WithoutCancel(ctx), previousExists)
		}
		return fmt.Errorf("activate xray configuration: %w", err)
	}
	if a.autoStart {
		started, startErr := a.startLocked(ctx, a.activePath)
		if startErr != nil {
			// Restore the file and the previous process before returning. The
			// reconciler will call Rollback as a second safety net.
			restoreErr := a.restoreAfterStartFailure(context.WithoutCancel(ctx), previous, previousExists, oldRunning)
			if restoreErr != nil {
				return fmt.Errorf("%w; rollback failed", startErr)
			}
			return startErr
		}
		a.process = started
	}
	return nil
}

func (a *XrayProcessAdapter) Verify(ctx context.Context, prepared PreparedConfiguration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.checkOwner(prepared); err != nil {
		return err
	}
	if err := a.verifyActive(prepared); err != nil {
		return fmt.Errorf("verify active xray configuration: %w", err)
	}
	if a.autoStart && (a.process == nil || !a.process.Running()) {
		return ErrXrayNotRunning
	}
	return nil
}

// ActiveEngineMode attests only a process owned and currently running under
// this adapter. Staging or validating a configuration is not deployment.
func (a *XrayProcessAdapter) ActiveEngineMode() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.autoStart && a.process != nil && a.process.Running() {
		return "xray"
	}
	return ""
}

func (a *XrayProcessAdapter) RequiresLiveProcess() bool { return a.autoStart }

func (a *XrayProcessAdapter) Rollback(ctx context.Context, previous *PreparedConfiguration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.autoStart && a.process != nil && a.process.Running() {
		if err := a.process.Stop(context.WithoutCancel(ctx)); err != nil {
			return fmt.Errorf("stop xray process for rollback: %w", err)
		}
		a.process = nil
	}
	if previous == nil {
		if err := removeRegularFile(a.activePath); err != nil {
			return fmt.Errorf("remove xray configuration during rollback: %w", err)
		}
		return nil
	}
	if err := a.checkOwner(*previous); err != nil {
		return err
	}
	if err := a.verifyStaged(*previous); err != nil {
		return fmt.Errorf("validate rollback xray configuration: %w", err)
	}
	previousBytes, err := readRegularFile(previous.adapterPath, MaxConfigurationBytes)
	if err != nil {
		return fmt.Errorf("read rollback xray configuration: %w", err)
	}
	if err := writeFileAtomic(a.activePath, previousBytes, 0o600); err != nil {
		return fmt.Errorf("restore xray configuration: %w", err)
	}
	if a.autoStart {
		started, startErr := a.startLocked(context.WithoutCancel(ctx), a.activePath)
		if startErr != nil {
			return startErr
		}
		a.process = started
	}
	return nil
}

// Close stops a managed process. It is intentionally not part of
// EngineAdapter so existing dry-run callers remain source-compatible.
func (a *XrayProcessAdapter) Close(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.process == nil {
		return nil
	}
	err := a.process.Stop(ctx)
	a.process = nil
	return err
}

func (a *XrayProcessAdapter) checkOwner(prepared PreparedConfiguration) error {
	if len(prepared.ConfigSHA256) < 16 {
		return ErrPreparedForOtherEngine
	}
	path := filepath.Clean(prepared.adapterPath)
	expected := filepath.Join(a.stagingDir, fmt.Sprintf("node-bundle-%d-%s.json", prepared.Generation, prepared.ConfigSHA256[:16]))
	if path == a.stagingDir || path != expected {
		return ErrPreparedForOtherEngine
	}
	return nil
}

func (a *XrayProcessAdapter) verifyStaged(prepared PreparedConfiguration) error {
	data, err := readRegularFile(prepared.adapterPath, MaxConfigurationBytes)
	if err != nil {
		return err
	}
	expected, err := compileXrayBundle(prepared.Bundle)
	if err != nil {
		return err
	}
	if !bytes.Equal(data, expected) {
		return ErrConfigurationHash
	}
	return nil
}

func (a *XrayProcessAdapter) verifyActive(prepared PreparedConfiguration) error {
	data, err := readRegularFile(a.activePath, MaxConfigurationBytes)
	if err != nil {
		return err
	}
	expected, err := compileXrayBundle(prepared.Bundle)
	if err != nil {
		return err
	}
	if !bytes.Equal(data, expected) {
		return ErrConfigurationHash
	}
	return nil
}

func (a *XrayProcessAdapter) startLocked(ctx context.Context, path string) (XrayProcess, error) {
	process, err := a.runner.Start(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("%w", ErrXrayStart)
	}
	if process == nil {
		return nil, ErrXrayStart
	}
	a.counterEpoch++
	return process, nil
}

func (a *XrayProcessAdapter) restoreAfterStartFailure(ctx context.Context, previous []byte, previousExists, restart bool) error {
	if previousExists {
		if err := writeFileAtomic(a.activePath, previous, 0o600); err != nil {
			return err
		}
	} else if err := removeRegularFile(a.activePath); err != nil {
		return err
	}
	return a.restartPrevious(ctx, previousExists && restart)
}

func (a *XrayProcessAdapter) restartPrevious(ctx context.Context, restart bool) error {
	if !restart {
		return nil
	}
	started, err := a.startLocked(ctx, a.activePath)
	if err != nil {
		return err
	}
	a.process = started
	return nil
}

func readOptionalRegularFile(path string) ([]byte, bool, error) {
	data, err := readRegularFile(path, MaxConfigurationBytes)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

// ExecXrayRunner invokes a fixed absolute binary with fixed arguments. It
// never uses a shell and discards stdout/stderr so engine diagnostics cannot
// leak runtime credentials into agent logs.
type ExecXrayRunner struct {
	binaryPath string
}

func NewExecXrayRunner(binaryPath string) (*ExecXrayRunner, error) {
	binaryPath = filepath.Clean(strings.TrimSpace(binaryPath))
	if binaryPath == "." || !filepath.IsAbs(binaryPath) {
		return nil, errors.New("xray binary path must be an absolute path")
	}
	info, err := os.Lstat(binaryPath)
	if err != nil {
		return nil, fmt.Errorf("inspect xray binary: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("xray binary path must be a regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return nil, errors.New("xray binary is not executable")
	}
	return &ExecXrayRunner{binaryPath: binaryPath}, nil
}

func (r *ExecXrayRunner) Test(ctx context.Context, configPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	command := exec.CommandContext(ctx, r.binaryPath, "run", "-test", "-config", configPath)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		return ErrXrayValidation
	}
	return nil
}

func (r *ExecXrayRunner) Start(ctx context.Context, configPath string) (XrayProcess, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	command := exec.Command(r.binaryPath, "run", "-config", configPath)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		return nil, ErrXrayStart
	}
	process := &execXrayProcess{command: command, done: make(chan error, 1)}
	go func() { process.done <- command.Wait() }()
	// Detect immediate startup failure without imposing an arbitrary readiness
	// delay. Verify observes a process that exits shortly afterwards.
	select {
	case <-process.done:
		return nil, ErrXrayStart
	default:
		return process, nil
	}
}

type execXrayProcess struct {
	command *exec.Cmd
	done    chan error
}

func (p *execXrayProcess) Running() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

func (p *execXrayProcess) Stop(ctx context.Context) error {
	if !p.Running() {
		return nil
	}
	if err := p.command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// CompileXrayBundle combines only Xray-native fragments. Inbounds, outbounds,
// and routing rules are concatenated; other settings must agree across fragments.
func CompileXrayBundle(bundle agentv1.ConfigurationBundle) ([]byte, error) {
	if bundle.SchemaVersion != BundleSchemaVersion {
		return nil, fmt.Errorf("%w: unsupported bundle schema_version %d", ErrInvalidConfiguration, bundle.SchemaVersion)
	}
	if bundle.Fragments == nil {
		return nil, fmt.Errorf("%w: fragments must be an array", ErrInvalidConfiguration)
	}
	merged := make(map[string]json.RawMessage)
	arrayValues := map[string][]json.RawMessage{"inbounds": {}, "outbounds": {}}
	routingRules := make([]json.RawMessage, 0)
	routingSettings := make(map[string]json.RawMessage)
	hasRouting := false
	for _, fragment := range bundle.Fragments {
		if fragment.Engine != agentv1.EngineXray {
			return nil, fmt.Errorf("%w: fragment %q uses %q", ErrXrayUnsupportedFragment, fragment.GroupID, fragment.Engine)
		}
		var object map[string]json.RawMessage
		if err := decodeJSON(fragment.Config, &object); err != nil || object == nil {
			return nil, fmt.Errorf("%w: fragment %q is not a JSON object", ErrInvalidConfiguration, fragment.GroupID)
		}
		for key, raw := range object {
			if key == "routing" {
				var routing map[string]json.RawMessage
				if err := decodeJSON(raw, &routing); err != nil || routing == nil {
					return nil, fmt.Errorf("%w: routing must be an object", ErrXrayConfigConflict)
				}
				hasRouting = true
				var rules []json.RawMessage
				if err := decodeJSON(routing["rules"], &rules); err != nil || rules == nil {
					return nil, fmt.Errorf("%w: routing.rules must be an array", ErrXrayConfigConflict)
				}
				routingRules = append(routingRules, rules...)
				for setting, value := range routing {
					if setting == "rules" {
						continue
					}
					canonical, err := canonicalJSON(value)
					if err != nil {
						return nil, fmt.Errorf("%w: invalid routing.%s", ErrInvalidConfiguration, setting)
					}
					if previous, exists := routingSettings[setting]; exists && !bytes.Equal(previous, canonical) {
						return nil, fmt.Errorf("%w: routing.%s differs between fragments", ErrXrayConfigConflict, setting)
					}
					routingSettings[setting] = canonical
				}
				continue
			}
			if _, isArray := arrayValues[key]; isArray {
				var values []json.RawMessage
				if err := decodeJSON(raw, &values); err != nil || values == nil {
					return nil, fmt.Errorf("%w: %s must be an array", ErrXrayConfigConflict, key)
				}
				arrayValues[key] = append(arrayValues[key], values...)
				continue
			}
			canonical, err := canonicalJSON(raw)
			if err != nil {
				return nil, fmt.Errorf("%w: invalid %s", ErrInvalidConfiguration, key)
			}
			if previous, exists := merged[key]; exists {
				previousCanonical, _ := canonicalJSON(previous)
				if !bytes.Equal(previousCanonical, canonical) {
					return nil, fmt.Errorf("%w: top-level key %q differs between fragments", ErrXrayConfigConflict, key)
				}
				continue
			}
			merged[key] = canonical
		}
	}
	seenTags := make(map[string]struct{})
	for key, values := range arrayValues {
		if err := validateXrayArray(key, values, seenTags); err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(values)
		if err != nil {
			return nil, fmt.Errorf("%w: encode %s", ErrInvalidConfiguration, key)
		}
		merged[key] = encoded
	}
	if hasRouting {
		encoded, err := json.Marshal(routingRules)
		if err != nil {
			return nil, fmt.Errorf("%w: encode routing rules", ErrInvalidConfiguration)
		}
		routingSettings["rules"] = encoded
		encoded, err = json.Marshal(routingSettings)
		if err != nil {
			return nil, fmt.Errorf("%w: encode routing", ErrInvalidConfiguration)
		}
		merged["routing"] = encoded
	}
	// StatsService is a bundle-level capability, not an optional fragment
	// detail. Older desired configurations predate the api/stats fields; the
	// agent must upgrade those bundles in memory as well, otherwise traffic
	// accounting remains permanently at zero until every rule is recreated.
	if err := ensureXrayStatsPolicy(merged); err != nil {
		return nil, err
	}
	merged["api"] = json.RawMessage(`{"tag":"hl-stats-api","services":["StatsService"]}`)
	merged["stats"] = json.RawMessage(`{}`)
	// Xray's API service is exposed through one loopback-only dokodemo-door
	// listener. The bundle compiler owns the singleton listener to avoid port
	// and tag collisions when several rules are active on the same node.
	apiInbound, _ := json.Marshal(map[string]any{
		"tag": "hl-stats-api-in", "listen": "127.0.0.1", "port": 10085,
		"protocol": "dokodemo-door", "settings": map[string]any{"address": "127.0.0.1"},
	})
	apiOutbound, _ := json.Marshal(map[string]any{"tag": "hl-stats-api", "protocol": "freedom"})
	// Validate only the newly owned API entries. The existing arrays have
	// already been validated above; validating the full append with the same
	// seenTags map would report every existing tag as a duplicate.
	if err := validateXrayArray("inbounds", []json.RawMessage{apiInbound}, seenTags); err != nil {
		return nil, err
	}
	if err := validateXrayArray("outbounds", []json.RawMessage{apiOutbound}, seenTags); err != nil {
		return nil, err
	}
	arrayValues["inbounds"] = append(arrayValues["inbounds"], apiInbound)
	arrayValues["outbounds"] = append(arrayValues["outbounds"], apiOutbound)
	mergedInbounds, err := json.Marshal(arrayValues["inbounds"])
	if err != nil {
		return nil, fmt.Errorf("%w: encode inbounds", ErrInvalidConfiguration)
	}
	mergedOutbounds, err := json.Marshal(arrayValues["outbounds"])
	if err != nil {
		return nil, fmt.Errorf("%w: encode outbounds", ErrInvalidConfiguration)
	}
	merged["inbounds"], merged["outbounds"] = mergedInbounds, mergedOutbounds
	if !hasRouting {
		routingSettings = make(map[string]json.RawMessage)
	}
	routingRules = append(routingRules, json.RawMessage(`{"type":"field","inboundTag":["hl-stats-api-in"],"outboundTag":"hl-stats-api"}`))
	routingSettings["rules"], _ = json.Marshal(routingRules)
	mergedRouting, err := json.Marshal(routingSettings)
	if err != nil {
		return nil, fmt.Errorf("%w: encode routing", ErrInvalidConfiguration)
	}
	merged["routing"] = mergedRouting
	result, err := json.Marshal(merged)
	if err != nil {
		return nil, fmt.Errorf("%w: encode merged xray configuration", ErrInvalidConfiguration)
	}
	return result, nil
}

func ensureXrayStatsPolicy(merged map[string]json.RawMessage) error {
	policy := make(map[string]json.RawMessage)
	if raw, exists := merged["policy"]; exists {
		if err := decodeJSON(raw, &policy); err != nil || policy == nil {
			return fmt.Errorf("%w: policy must be an object", ErrXrayConfigConflict)
		}
	}
	system := make(map[string]json.RawMessage)
	if raw, exists := policy["system"]; exists {
		if err := decodeJSON(raw, &system); err != nil || system == nil {
			return fmt.Errorf("%w: policy.system must be an object", ErrXrayConfigConflict)
		}
	}
	system["statsInboundUplink"] = json.RawMessage("true")
	system["statsInboundDownlink"] = json.RawMessage("true")
	system["statsOutboundUplink"] = json.RawMessage("true")
	system["statsOutboundDownlink"] = json.RawMessage("true")
	encodedSystem, err := json.Marshal(system)
	if err != nil {
		return fmt.Errorf("%w: encode policy.system", ErrInvalidConfiguration)
	}
	policy["system"] = encodedSystem
	encodedPolicy, err := json.Marshal(policy)
	if err != nil {
		return fmt.Errorf("%w: encode policy", ErrInvalidConfiguration)
	}
	merged["policy"] = encodedPolicy
	return nil
}

// compileXrayBundle is kept as a local alias for adapter internals and tests.
func compileXrayBundle(bundle agentv1.ConfigurationBundle) ([]byte, error) {
	return CompileXrayBundle(bundle)
}

func validateXrayArray(key string, values []json.RawMessage, seenTags map[string]struct{}) error {
	for index, raw := range values {
		var object map[string]json.RawMessage
		if err := decodeJSON(raw, &object); err != nil || object == nil {
			return fmt.Errorf("%w: %s[%d] must be an object", ErrXrayConfigConflict, key, index)
		}
		if rawTag, exists := object["tag"]; exists {
			var tag string
			if err := json.Unmarshal(rawTag, &tag); err != nil || strings.TrimSpace(tag) == "" {
				return fmt.Errorf("%w: %s[%d] has an invalid tag", ErrXrayConfigConflict, key, index)
			}
			if _, exists := seenTags[tag]; exists {
				return fmt.Errorf("%w: duplicate tag %q", ErrXrayConfigConflict, tag)
			}
			seenTags[tag] = struct{}{}
		}
	}
	return nil
}

func canonicalJSON(raw []byte) ([]byte, error) {
	var value any
	if err := decodeJSON(raw, &value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

// CounterEpoch changes only when this adapter starts a new owned process.
func (a *XrayProcessAdapter) CounterEpoch() uint64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.counterEpoch
}
