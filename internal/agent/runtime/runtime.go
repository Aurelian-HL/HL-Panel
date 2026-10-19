// Package runtime assembles the long-lived edge-agent loops around the
// control client, state store, reconciler, and bounded engine adapter.
package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/hongle/hl-panel/internal/agent/hostprobe"
	"log/slog"
	"os"
	goruntime "runtime"
	"strings"
	"sync"
	"time"

	"github.com/hongle/hl-panel/internal/agent/config"
	"github.com/hongle/hl-panel/internal/agent/controlclient"
	"github.com/hongle/hl-panel/internal/agent/engine"
	"github.com/hongle/hl-panel/internal/agent/probeecho"
	"github.com/hongle/hl-panel/internal/agent/reconciler"
	"github.com/hongle/hl-panel/internal/agent/state"
	"github.com/hongle/hl-panel/internal/agent/trafficcontrol"
	usageagent "github.com/hongle/hl-panel/internal/agent/usage"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

type Agent struct {
	cfg            config.Config
	client         *controlclient.Client
	credential     *state.CredentialStore
	state          *state.AtomicStateStore
	adapter        engine.EngineAdapter
	xrayAdapter    *engine.XrayProcessAdapter
	gostAdapter    *engine.GOSTProcessAdapter
	engineMode     string
	reconciler     *reconciler.Reconciler
	nodeID         string
	nodeSecret     string
	bootID         string
	logger         *slog.Logger
	bootstrapMu    sync.Mutex
	usageApplyMu   sync.Mutex
	usageSource    usageagent.CounterSource
	accessExecutor usageagent.AccessExecutor
	usageReporter  *usageagent.Reporter
	enforcement    *usageagent.EnforcementReconciler
	trafficControl *trafficcontrol.Controller
	probeEchoMu    sync.RWMutex
	probeEcho      *probeecho.Server
}

type Options struct {
	UsageSource         usageagent.CounterSource
	EngineUsageSources  map[agentv1.Engine]usageagent.CounterSource
	EnforcementExecutor usageagent.AccessExecutor
}

func New(cfg config.Config, logger *slog.Logger) (*Agent, error) {
	return NewWithOptions(cfg, logger, Options{})
}

// NewWithOptions is an internal integration hook. It must never turn an
// injected fixture into production accounting or acknowledge a no-op limit.
func NewWithOptions(cfg config.Config, logger *slog.Logger, options Options) (*Agent, error) {
	if err := validateUsageOptions(cfg, options); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}
	httpClient, err := cfg.HTTPClient()
	if err != nil {
		return nil, err
	}
	client, err := controlclient.NewWithOptions(cfg.ControlPlaneURL, httpClient, cfg.AllowInsecureLoopback)
	if err != nil {
		return nil, err
	}
	var adapter engine.EngineAdapter
	var trafficController *trafficcontrol.Controller
	var xrayAdapter *engine.XrayProcessAdapter
	var gostAdapter *engine.GOSTProcessAdapter
	switch cfg.EngineMode {
	case config.EngineModeXray:
		xrayAdapter, err = engine.NewXrayProcessAdapter(engine.XrayAdapterOptions{
			StagingDir: cfg.XrayStagingDir(), ActivePath: cfg.XrayActivePath(),
			BinaryPath: cfg.XrayBinaryPath, AutoStart: cfg.XrayAutoStart,
			StatsAPIAddress: cfg.XrayStatsAPIAddress,
		})
		if err != nil {
			return nil, fmt.Errorf("initialize xray engine adapter: %w", err)
		}
		adapter = xrayAdapter
	case config.EngineModeGOST:
		gostAdapter, err = engine.NewGOSTProcessAdapter(engine.GOSTAdapterOptions{
			StagingDir: cfg.GOSTStagingDir(), ActivePath: cfg.GOSTActivePath(),
			BinaryPath: cfg.GOSTBinaryPath, AutoStart: cfg.GOSTAutoStart,
		})
		if err != nil {
			return nil, fmt.Errorf("initialize gost engine adapter: %w", err)
		}
		adapter = gostAdapter
	case config.EngineModeMixed:
		xrayAdapter, err = engine.NewXrayProcessAdapter(engine.XrayAdapterOptions{
			StagingDir: cfg.XrayStagingDir(), ActivePath: cfg.XrayActivePath(),
			BinaryPath: cfg.XrayBinaryPath, AutoStart: cfg.XrayAutoStart,
			StatsAPIAddress: cfg.XrayStatsAPIAddress,
		})
		if err != nil {
			return nil, fmt.Errorf("initialize xray engine adapter: %w", err)
		}
		gostAdapter, err = engine.NewGOSTProcessAdapter(engine.GOSTAdapterOptions{
			StagingDir: cfg.GOSTStagingDir(), ActivePath: cfg.GOSTActivePath(),
			BinaryPath: cfg.GOSTBinaryPath, AutoStart: cfg.GOSTAutoStart,
		})
		if err != nil {
			return nil, fmt.Errorf("initialize gost engine adapter: %w", err)
		}
		adapter, err = engine.NewMixedProcessAdapter(xrayAdapter, gostAdapter)
		if err != nil {
			return nil, fmt.Errorf("initialize mixed engine adapter: %w", err)
		}
	default:
		adapter, err = engine.NewDryRunFileAdapter(cfg.EngineStagingPath(), cfg.EngineActivePath())
		if err != nil {
			return nil, err
		}
	}
	if strings.TrimSpace(cfg.TrafficControlInterface) != "" {
		trafficController, err = trafficcontrol.New(cfg.TrafficControlInterface, cfg.TrafficControlBinary, cfg.TrafficControlStateFile, nil)
		if err != nil {
			return nil, fmt.Errorf("initialize traffic control: %w", err)
		}
	}
	if len(options.EngineUsageSources) > 0 {
		var sources []usageagent.CounterSource
		for _, kind := range []agentv1.Engine{agentv1.EngineXray, agentv1.EngineGOST} {
			source := options.EngineUsageSources[kind]
			if source == nil {
				continue
			}
			switch kind {
			case agentv1.EngineXray:
				if xrayAdapter != nil {
					source = &usageagent.EpochSource{Source: source, Epoch: xrayAdapter.CounterEpoch}
				}
			case agentv1.EngineGOST:
				if gostAdapter != nil {
					source = &usageagent.EpochSource{Source: source, Epoch: gostAdapter.CounterEpoch}
				}
			}
			if expected, ok := adapter.(interface{ ExpectsEngine(agentv1.Engine) bool }); ok {
				engineKind := kind
				source = usageagent.ConditionalSource{Source: source, Expected: func() bool { return expected.ExpectsEngine(engineKind) }}
			}
			sources = append(sources, source)
		}
		options.UsageSource, err = usageagent.NewMultiSource(sources...)
		if err != nil {
			return nil, err
		}
	}
	bootID := ""
	if options.UsageSource != nil {
		journal, err := usageagent.NewJournalStore(cfg.UsageJournalPath()).Prepare(time.Now().UTC())
		if err != nil {
			return nil, fmt.Errorf("prepare usage journal: %w", err)
		}
		bootID = journal.BootID
	} else {
		bootID, err = newBootID()
		if err != nil {
			return nil, fmt.Errorf("create boot id: %w", err)
		}
	}
	return &Agent{
		cfg:            cfg,
		client:         client,
		credential:     state.NewCredentialStore(cfg.CredentialPath()),
		state:          state.NewAtomicStateStore(cfg.StatePath(), cfg.ConfigurationDir()),
		adapter:        adapter,
		xrayAdapter:    xrayAdapter,
		gostAdapter:    gostAdapter,
		engineMode:     cfg.EngineMode,
		bootID:         bootID,
		logger:         logger,
		usageSource:    options.UsageSource,
		accessExecutor: options.EnforcementExecutor,
		trafficControl: trafficController,
	}, nil
}

func validateUsageOptions(cfg config.Config, options Options) error {
	if options.UsageSource == nil && len(options.EngineUsageSources) == 0 && options.EnforcementExecutor == nil {
		return nil
	}
	if (options.UsageSource != nil || len(options.EngineUsageSources) > 0) && cfg.EngineMode != config.EngineModeXray && cfg.EngineMode != config.EngineModeGOST && cfg.EngineMode != config.EngineModeMixed {
		return errors.New("usage collection requires an Xray or GOST engine")
	}
	if options.EnforcementExecutor != nil && (cfg.EngineMode != config.EngineModeXray && cfg.EngineMode != config.EngineModeMixed || !cfg.XrayAutoStart) {
		return errors.New("usage enforcement requires an agent-owned Xray process")
	}
	return nil
}

// Close releases a locally managed data-plane process. Dry-run mode has no
// process to stop; callers should invoke this during graceful shutdown.
func (a *Agent) Close(ctx context.Context) error {
	probeErr := a.closeProbeEcho()
	// Close must serialize with apply, administrator stop/restart, and the
	// recovery loop. Otherwise recovery can observe the process disappearing
	// while Close is still running and start it again during shutdown.
	a.usageApplyMu.Lock()
	defer a.usageApplyMu.Unlock()
	if a.xrayAdapter != nil {
		xrayErr := a.xrayAdapter.Close(ctx)
		if a.gostAdapter != nil {
			return errors.Join(probeErr, xrayErr, a.gostAdapter.Close(ctx))
		}
		return errors.Join(probeErr, xrayErr)
	}
	if a.gostAdapter != nil {
		return errors.Join(probeErr, a.gostAdapter.Close(ctx))
	}
	return probeErr
}

func (a *Agent) closeProbeEcho() error {
	a.probeEchoMu.Lock()
	probe := a.probeEcho
	a.probeEcho = nil
	a.probeEchoMu.Unlock()
	if probe == nil {
		return nil
	}
	return probe.Close()
}

func (a *Agent) probeEchoRunning() bool {
	a.probeEchoMu.Lock()
	probe := a.probeEcho
	if probe == nil {
		a.probeEchoMu.Unlock()
		return false
	}
	select {
	case <-probe.Done():
		if a.probeEcho == probe {
			a.probeEcho = nil
		}
		a.probeEchoMu.Unlock()
		return false
	default:
		a.probeEchoMu.Unlock()
		return true
	}
}

func (a *Agent) startProbeEcho(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	probe, err := probeecho.Listen(a.cfg.ProtocolProbeEchoPort)
	if err != nil {
		return err
	}
	a.probeEchoMu.Lock()
	select {
	case <-ctx.Done():
		a.probeEchoMu.Unlock()
		_ = probe.Close()
		return ctx.Err()
	default:
	}
	if a.probeEcho != nil {
		a.probeEchoMu.Unlock()
		_ = probe.Close()
		return nil
	}
	a.probeEcho = probe
	a.probeEchoMu.Unlock()
	a.logger.Info("protocol probe echo listening", "port", a.cfg.ProtocolProbeEchoPort)
	return nil
}

const defaultProbeEchoRetryInterval = 5 * time.Second

var probeEchoRetryInterval = defaultProbeEchoRetryInterval

// probeEchoLoop keeps the optional local protocol target available without
// coupling its lifecycle to heartbeat, desired configuration, or usage loops.
// A transient bind conflict therefore self-heals after the owning process exits.
func (a *Agent) probeEchoLoop(ctx context.Context) error {
	defer func() { _ = a.closeProbeEcho() }()
	ticker := time.NewTicker(probeEchoRetryInterval)
	defer ticker.Stop()
	for {
		if !a.probeEchoRunning() {
			if err := a.startProbeEcho(ctx); err != nil && !errors.Is(err, context.Canceled) {
				a.logger.Warn("protocol probe echo unavailable; will retry", "port", a.cfg.ProtocolProbeEchoPort, "error", err)
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (a *Agent) Bootstrap(ctx context.Context) error {
	a.bootstrapMu.Lock()
	defer a.bootstrapMu.Unlock()
	if a.reconciler != nil {
		return nil
	}
	if err := a.credential.Prepare(); err != nil {
		return fmt.Errorf("prepare credential store: %w", err)
	}
	credentials, err := a.credential.Load()
	if errors.Is(err, state.ErrCredentialsNotFound) {
		credentials, err = a.enroll(ctx)
	}
	if err != nil {
		return err
	}
	if err := a.credential.ClearEnrollmentAttempt(); err != nil {
		return fmt.Errorf("remove completed enrollment attempt: %w", err)
	}
	a.nodeID = credentials.NodeID
	a.nodeSecret = credentials.NodeCredential
	reconcilerInstance, err := reconciler.New(a.state, a.adapter, a.client, a.nodeSecret)
	if err != nil {
		return err
	}
	if a.usageSource != nil {
		maxBatch := a.cfg.UsageMaxBatch
		if maxBatch == 0 {
			maxBatch = 500
		}
		a.usageReporter, err = usageagent.NewReporter(usageagent.NewJournalStore(a.cfg.UsageJournalPath()), a.usageSource,
			a.client, a.nodeID, a.nodeSecret, maxBatch, time.Now)
		if err != nil {
			return fmt.Errorf("initialize usage reporter: %w", err)
		}
	}
	if a.usageReporter != nil {
		a.usageReporter.SetGenerationProvider(a.usageGeneration)
	}
	if a.accessExecutor != nil {
		a.enforcement, err = usageagent.NewEnforcementReconciler(a.client, a.accessExecutor, a.nodeSecret)
		if err != nil {
			return fmt.Errorf("initialize enforcement reconciler: %w", err)
		}
	}
	_, hadPending, err := a.state.PendingDesired()
	if err != nil {
		return fmt.Errorf("load pending configuration: %w", err)
	}
	stopped, err := a.state.EngineStopped()
	if err != nil {
		return fmt.Errorf("load engine intent: %w", err)
	}
	if !stopped {
		if err := reconcilerInstance.RecoverPending(ctx); err != nil {
			return fmt.Errorf("recover pending configuration: %w", err)
		}
		if !hadPending {
			if _, err := reconcilerInstance.EnsureApplied(ctx); err != nil {
				return fmt.Errorf("restore applied configuration: %w", err)
			}
		}
		if a.trafficControl != nil {
			if err := a.trafficControl.Restore(ctx); err != nil {
				return fmt.Errorf("restore traffic control: %w", err)
			}
		}
	}
	a.reconciler = reconcilerInstance
	return nil
}

func (a *Agent) enroll(ctx context.Context) (state.Credentials, error) {
	token := strings.TrimSpace(os.Getenv(a.cfg.EnrollmentTokenEnv))
	if token == "" {
		return state.Credentials{}, fmt.Errorf("enrollment token environment variable %q is empty", a.cfg.EnrollmentTokenEnv)
	}
	secret, err := a.credential.EnrollmentSecret(a.cfg.ControlPlaneURL, token)
	if err != nil {
		return state.Credentials{}, fmt.Errorf("prepare enrollment recovery: %w", err)
	}
	response, err := a.client.Enroll(ctx, agentv1.EnrollmentRequest{
		EnrollmentToken:  token,
		EnrollmentSecret: secret,
		Hostname:         a.cfg.Hostname,
		DialHost:         a.cfg.DialHost,
		Platform:         goruntime.GOOS,
		Architecture:     goruntime.GOARCH,
		AgentVersion:     a.cfg.AgentVersion,
		Capabilities:     a.capabilities(),
	})
	if err != nil {
		return state.Credentials{}, fmt.Errorf("enroll node: %w", err)
	}
	if err := a.credential.SaveOnce(response, time.Now()); err != nil {
		return state.Credentials{}, fmt.Errorf("persist node credential: %w", err)
	}
	return a.credential.Load()
}

func (a *Agent) HeartbeatOnce(ctx context.Context) error {
	if err := a.Bootstrap(ctx); err != nil {
		return err
	}
	current, err := a.state.Load()
	if err != nil {
		return fmt.Errorf("load state for heartbeat: %w", err)
	}
	hostprobe.SetTCPGeneration(current.AppliedGeneration())
	request := agentv1.HeartbeatRequest{
		BootID:            a.bootID,
		Hostname:          a.cfg.Hostname,
		Platform:          goruntime.GOOS,
		Architecture:      goruntime.GOARCH,
		AgentVersion:      a.cfg.AgentVersion,
		Capabilities:      a.capabilities(),
		EngineVersions:    a.engineVersions(),
		Resources:         resourceSnapshot(),
		AppliedGeneration: current.AppliedGeneration(),
	}
	if current.Applied != nil {
		request.AppliedConfigSHA256 = current.Applied.ConfigSHA256
	}
	request.LastApplyPhase = current.LastApplyPhase
	request.LastApplyStatus = current.LastApplyStatus
	request.LastApplyMessage = current.LastApplyMessage
	return a.client.Heartbeat(ctx, a.nodeSecret, request)
}

func (a *Agent) engineVersions() map[string]string {
	xrayVersion, gostVersion := "unknown", "unknown"
	if a.xrayAdapter != nil {
		xrayVersion = a.xrayAdapter.EngineVersion()
	}
	if a.gostAdapter != nil {
		gostVersion = a.gostAdapter.EngineVersion()
	}
	if a.engineMode == config.EngineModeMixed {
		return map[string]string{"node-bundle": "mixed-process-adapter", "xray": xrayVersion, "gost": gostVersion}
	}
	if a.engineMode == config.EngineModeXray {
		return map[string]string{"node-bundle": "xray-process-adapter", "xray": xrayVersion}
	}
	if a.engineMode == config.EngineModeGOST {
		return map[string]string{"node-bundle": "gost-process-adapter", "gost": gostVersion}
	}
	return map[string]string{"node-bundle": "dry-run-file-adapter"}
}

func (a *Agent) DesiredOnce(ctx context.Context) (bool, error) {
	if err := a.Bootstrap(ctx); err != nil {
		return false, err
	}
	desired, err := a.client.Desired(ctx, a.nodeSecret)
	if err != nil {
		return false, err
	}
	if desired == nil {
		return false, a.reconciler.FlushReceipts(ctx)
	}
	if err := a.applyWithUsage(ctx, *desired); err != nil {
		return true, err
	}
	return true, a.reconciler.FlushReceipts(ctx)
}

func (a *Agent) UsageOnce(ctx context.Context) error {
	if err := a.Bootstrap(ctx); err != nil {
		return err
	}
	if a.usageReporter == nil {
		return usageagent.ErrCounterSourceUnavailable
	}
	return a.flushUsage(ctx)
}

func (a *Agent) EnforcementOnce(ctx context.Context) (bool, error) {
	if err := a.Bootstrap(ctx); err != nil {
		return false, err
	}
	if a.enforcement == nil {
		return false, usageagent.ErrEnforcementExecutorUnavailable
	}
	if err := a.requireOwnedXray(); err != nil {
		return false, err
	}
	return a.enforcement.ReconcileOnce(ctx)
}

// ControlOnce consumes one closed-set administrator command. Commands are
// reported as durable results so a lost response can be retried safely.
func (a *Agent) ControlOnce(ctx context.Context) error {
	if err := a.Bootstrap(ctx); err != nil {
		return err
	}
	command, err := a.client.DesiredControl(ctx, a.nodeSecret)
	if err != nil || command == nil {
		return err
	}
	result := agentv1.ControlCommandResultRequest{ID: command.ID, Status: "succeeded"}
	switch command.Command {
	case agentv1.ControlStatus:
		result.Message = "运行中"
		if mode := a.activeEngineMode(); mode != "" {
			result.Message = "运行中（" + mode + "）"
		} else {
			result.Message = "已停止"
		}
	case agentv1.ControlVersion:
		result.Message = "Agent " + a.cfg.AgentVersion + "; " + a.activeEngineMode()
	case agentv1.ControlLogs:
		current, loadErr := a.state.Load()
		if loadErr != nil {
			result.Status, result.Message = "failed", "读取节点应用记录失败"
		} else {
			result.Message = "最近配置应用记录"
			result.Logs = current.LastApplyMessage
			if result.Logs == "" {
				result.Logs = "暂无配置应用记录"
			}
		}
	case agentv1.ControlStop:
		if err := a.stopEngine(ctx); err != nil {
			result.Status, result.Message = "failed", "停止引擎失败"
		} else {
			result.Message = "引擎已停止"
		}
	case agentv1.ControlRestart:
		if err := a.restartEngine(ctx); err != nil {
			result.Status, result.Message = "failed", "恢复配置并重启失败"
		} else {
			result.Message = "引擎已重启并恢复最近配置"
		}
	default:
		result.Status, result.Message = "failed", "不支持的控制命令"
	}
	if len(result.Message) > 512 {
		result.Message = result.Message[:512]
	}
	return a.client.ReportControlResult(ctx, a.nodeSecret, result)
}

func (a *Agent) activeEngineMode() string {
	if process, ok := a.adapter.(interface{ ActiveEngineMode() string }); ok {
		return process.ActiveEngineMode()
	}
	return ""
}

func (a *Agent) closeEngines(ctx context.Context) error {
	var first error
	if a.xrayAdapter != nil {
		first = a.xrayAdapter.Close(ctx)
	}
	if a.gostAdapter != nil {
		if err := a.gostAdapter.Close(ctx); first == nil {
			first = err
		}
	}
	return first
}

func (a *Agent) requireOwnedXray() error {
	if a.xrayAdapter == nil || a.xrayAdapter.ActiveEngineMode() != config.EngineModeXray {
		return errors.New("usage engine unavailable: agent-owned Xray process is not running")
	}
	return nil
}

func (a *Agent) Run(ctx context.Context) error {
	if err := a.bootstrapWithRetry(ctx); err != nil {
		return err
	}
	// The first heartbeat reconciles persisted bundles after compiler/core
	// upgrades. Complete it before starting desired watches, otherwise the
	// watch can fetch an obsolete bundle while reconciliation is still pending.
	// Keep this best-effort and bounded: an unreachable panel must not prevent
	// last-known-good forwarding, recovery, receipts, or the local probe loop.
	initialContext, initialCancel := context.WithTimeout(ctx, 5*time.Second)
	if err := a.HeartbeatOnce(initialContext); err != nil && ctx.Err() == nil {
		a.logger.Warn("initial heartbeat failed", "error", err)
	}
	initialCancel()
	childContext, cancel := context.WithCancel(ctx)
	defer cancel()
	loops := []func(context.Context) error{a.heartbeatLoop, a.desiredLoop, a.controlLoop, a.receiptLoop, a.probeEchoLoop}
	if process, ok := a.adapter.(interface{ RequiresLiveProcess() bool }); ok && process.RequiresLiveProcess() {
		loops = append(loops, a.recoveryLoop)
	}
	if a.usageReporter != nil {
		loops = append(loops, a.usageLoop)
	}
	if a.enforcement != nil {
		loops = append(loops, a.enforcementLoop)
	}
	errorsChannel := make(chan error, len(loops))
	for _, loop := range loops {
		go func(run func(context.Context) error) { errorsChannel <- run(childContext) }(loop)
	}
	var firstError error
	for range loops {
		err := <-errorsChannel
		if err != nil && !errors.Is(err, context.Canceled) && firstError == nil {
			firstError = err
			cancel()
		}
	}
	if firstError != nil {
		return firstError
	}
	return ctx.Err()
}

func (a *Agent) controlLoop(ctx context.Context) error {
	interval := a.cfg.DesiredPollInterval.Duration()
	if interval <= 0 {
		interval = 10 * time.Second
	}
	backoff := reconciler.NewBackoff(a.cfg.Backoff)
	return runPeriodic(ctx, interval, backoff, a.ControlOnce, func(err error) { a.logger.Warn("control command failed", "error", err) })
}

func (a *Agent) receiptLoop(ctx context.Context) error {
	interval := a.cfg.DesiredPollInterval.Duration()
	if interval <= 0 || interval > 5*time.Second {
		interval = 5 * time.Second
	}
	settings := a.cfg.Backoff
	if settings.Maximum.Duration() <= 0 || settings.Maximum.Duration() > heartbeatRetryMaximum {
		settings.Maximum = config.Duration(heartbeatRetryMaximum)
	}
	if settings.Initial.Duration() <= 0 || settings.Initial.Duration() > settings.Maximum.Duration() {
		settings.Initial = settings.Maximum
	}
	backoff := reconciler.NewBackoff(settings)
	return runPeriodic(ctx, interval, backoff, func(callContext context.Context) error {
		return a.reconciler.FlushReceipts(callContext)
	}, func(err error) { a.logger.Warn("apply receipt delivery failed", "error", err) })
}

func (a *Agent) usageLoop(ctx context.Context) error {
	interval := a.cfg.UsageReportInterval.Duration()
	if interval <= 0 {
		interval = 30 * time.Second
	}
	backoff := reconciler.NewBackoff(a.cfg.Backoff)
	return runPeriodic(ctx, interval, backoff, func(callContext context.Context) error {
		return a.flushUsage(callContext)
	}, func(err error) { a.logger.Warn("usage reporting failed", "error", err) })
}

func (a *Agent) enforcementLoop(ctx context.Context) error {
	interval := a.cfg.EnforcementInterval.Duration()
	if interval <= 0 {
		interval = 10 * time.Second
	}
	backoff := reconciler.NewBackoff(a.cfg.Backoff)
	return runPeriodic(ctx, interval, backoff, func(callContext context.Context) error {
		if err := a.requireOwnedXray(); err != nil {
			return err
		}
		_, err := a.enforcement.ReconcileOnce(callContext)
		return err
	}, func(err error) { a.logger.Warn("usage enforcement failed", "error", err) })
}

func (a *Agent) heartbeatLoop(ctx context.Context) error {
	backoff := reconciler.NewBackoff(heartbeatBackoffConfig(a.cfg))
	return runPeriodicCadence(ctx, a.cfg.HeartbeatInterval.Duration(), backoff, func(callContext context.Context) error {
		return a.HeartbeatOnce(callContext)
	}, func(err error) {
		a.logger.Warn("heartbeat failed", "error", err)
	})
}

const heartbeatRetryMaximum = 15 * time.Second

func heartbeatBackoffConfig(cfg config.Config) config.BackoffConfig {
	settings := cfg.Backoff
	if settings.Maximum.Duration() > heartbeatRetryMaximum {
		settings.Maximum = config.Duration(heartbeatRetryMaximum)
	}
	if settings.Initial.Duration() > settings.Maximum.Duration() {
		settings.Initial = settings.Maximum
	}
	return settings
}

func (a *Agent) desiredLoop(ctx context.Context) error {
	backoff := reconciler.NewBackoff(a.cfg.Backoff)
	var failedKey string
	for {
		watchSupported := false
		watchWaited := false
		waitForPoll := false
		err := func(callContext context.Context) error {
			current, err := a.state.Load()
			if err != nil {
				return fmt.Errorf("load applied generation before desired watch: %w", err)
			}
			desired, supported, waited, err := a.client.DesiredWatch(callContext, a.nodeSecret, current.AppliedGeneration())
			watchSupported = supported
			watchWaited = waited
			if err != nil {
				return err
			}
			waitForPoll = !watchSupported
			if desired == nil {
				failedKey = ""
				// Some proxies and older control planes return 204 immediately
				// instead of holding the watch request. Avoid a tight reconnect loop
				// that can overload the panel and starve heartbeat traffic.
				waitForPoll = waitForPoll || !watchWaited
				return nil
			}
			key := fmt.Sprintf("%d/%s", desired.Generation, strings.ToLower(strings.TrimSpace(desired.ConfigSHA256)))
			if key == failedKey {
				// Give the failed generation one poll interval before retrying it.
				// Clear the marker here: a transient engine or filesystem failure
				// must not make this generation permanently unappliable.
				failedKey = ""
				waitForPoll = true
				return nil
			}
			if err := a.applyWithUsage(callContext, *desired); err != nil {
				if !errors.Is(err, errUsageBeforeApply) && !errors.Is(err, errEngineStopped) {
					failedKey = key
				}
				return err
			}
			failedKey = ""
			return nil
		}(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			a.logger.Warn("desired configuration sync failed", "error", err)
			if err := waitContext(ctx, backoff.Next()); err != nil {
				return err
			}
			continue
		}
		backoff.Reset()
		if waitForPoll {
			if err := waitContext(ctx, a.cfg.DesiredPollInterval.Duration()); err != nil {
				return err
			}
		}
	}
}

func runPeriodic(ctx context.Context, interval time.Duration, backoff *reconciler.Backoff, operation func(context.Context) error, report func(error)) error {
	return runPeriodicMode(ctx, interval, backoff, operation, report, false)
}

// Heartbeats sample on a fixed cadence without overlapping requests or adding
// request latency to each successful interval. Missed ticks never accumulate.
func runPeriodicCadence(ctx context.Context, interval time.Duration, backoff *reconciler.Backoff, operation func(context.Context) error, report func(error)) error {
	return runPeriodicMode(ctx, interval, backoff, operation, report, true)
}

func runPeriodicMode(ctx context.Context, interval time.Duration, backoff *reconciler.Backoff, operation func(context.Context) error, report func(error), cadence bool) error {
	for {
		started := time.Now()
		if err := operation(ctx); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				if ctx.Err() != nil {
					return ctx.Err()
				}
			}
			report(err)
			if err := waitContext(ctx, backoff.Next()); err != nil {
				return err
			}
			continue
		}
		backoff.Reset()
		delay := interval
		if cadence {
			delay = max(time.Millisecond, interval-time.Since(started))
		}
		if err := waitContext(ctx, delay); err != nil {
			return err
		}
	}
}

func waitContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func resourceSnapshot() agentv1.ResourceSnapshot {
	var memory goruntime.MemStats
	goruntime.ReadMemStats(&memory)
	return agentv1.ResourceSnapshot{
		Host:              hostprobe.Snapshot(),
		LogicalCPUs:       goruntime.NumCPU(),
		GoMaxProcs:        goruntime.GOMAXPROCS(0),
		MemoryAllocBytes:  memory.Alloc,
		MemorySystemBytes: memory.Sys,
	}
}

func newBootID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes[:]), nil
}

// Never trust a capability supplied in a config file for an unverified engine.
func (a *Agent) capabilities() []string {
	out := make([]string, 0, len(a.cfg.Capabilities)+1)
	for _, c := range a.cfg.NodeCapabilities() {
		if c != "gost-reliable-udp-v1" && c != config.CapabilityXrayVLESSUDPDirect {
			out = append(out, c)
		}
	}
	if (a.engineMode == config.EngineModeXray || a.engineMode == config.EngineModeMixed) &&
		a.xrayAdapter != nil && a.xrayAdapter.SupportsVLESSUDPDirect() {
		out = append(out, config.CapabilityXrayVLESSUDPDirect)
	}
	if a.gostAdapter != nil && a.gostAdapter.SupportsReliableUDP() {
		out = append(out, "gost-reliable-udp-v1")
	}
	return out
}
