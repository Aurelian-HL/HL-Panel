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
	var xrayAdapter *engine.XrayProcessAdapter
	var gostAdapter *engine.GOSTProcessAdapter
	switch cfg.EngineMode {
	case config.EngineModeXray:
		xrayAdapter, err = engine.NewXrayProcessAdapter(engine.XrayAdapterOptions{
			StagingDir: cfg.XrayStagingDir(), ActivePath: cfg.XrayActivePath(),
			BinaryPath: cfg.XrayBinaryPath, AutoStart: cfg.XrayAutoStart,
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
	var probeErr error
	if a.probeEcho != nil {
		probeErr = a.probeEcho.Close()
	}
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
	if err := reconcilerInstance.RecoverPending(ctx); err != nil {
		return fmt.Errorf("recover pending configuration: %w", err)
	}
	if !hadPending {
		if err := reconcilerInstance.RestoreApplied(ctx); err != nil {
			return fmt.Errorf("restore applied configuration: %w", err)
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
		Capabilities:     append([]string(nil), a.cfg.Capabilities...),
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
	request := agentv1.HeartbeatRequest{
		BootID:            a.bootID,
		Hostname:          a.cfg.Hostname,
		Platform:          goruntime.GOOS,
		Architecture:      goruntime.GOARCH,
		AgentVersion:      a.cfg.AgentVersion,
		Capabilities:      append([]string(nil), a.cfg.Capabilities...),
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
	if a.engineMode == config.EngineModeMixed {
		return map[string]string{"node-bundle": "mixed-process-adapter", "xray": "configured", "gost": "configured"}
	}
	if a.engineMode == config.EngineModeXray {
		return map[string]string{"node-bundle": "xray-process-adapter", "xray": "configured"}
	}
	if a.engineMode == config.EngineModeGOST {
		return map[string]string{"node-bundle": "gost-process-adapter", "gost": "configured"}
	}
	return map[string]string{"node-bundle": "dry-run-file-adapter"}
}

func (a *Agent) DesiredOnce(ctx context.Context) (bool, error) {
	if err := a.Bootstrap(ctx); err != nil {
		return false, err
	}
	if err := a.reconciler.FlushReceipts(ctx); err != nil {
		return false, fmt.Errorf("retry apply receipts: %w", err)
	}
	desired, err := a.client.Desired(ctx, a.nodeSecret)
	if err != nil {
		return false, err
	}
	if desired == nil {
		return false, nil
	}
	if err := a.applyWithUsage(ctx, *desired); err != nil {
		return true, err
	}
	return true, nil
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
	probe, err := probeecho.Listen(a.cfg.ProtocolProbeEchoPort)
	if err != nil {
		return err
	}
	a.probeEcho = probe
	childContext, cancel := context.WithCancel(ctx)
	defer cancel()
	loops := []func(context.Context) error{a.heartbeatLoop, a.desiredLoop}
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
	backoff := reconciler.NewBackoff(a.cfg.Backoff)
	return runPeriodic(ctx, a.cfg.HeartbeatInterval.Duration(), backoff, func(callContext context.Context) error {
		return a.HeartbeatOnce(callContext)
	}, func(err error) {
		a.logger.Warn("heartbeat failed", "error", err)
	})
}

func (a *Agent) desiredLoop(ctx context.Context) error {
	backoff := reconciler.NewBackoff(a.cfg.Backoff)
	var failedKey string
	return runPeriodic(ctx, a.cfg.DesiredPollInterval.Duration(), backoff, func(callContext context.Context) error {
		if err := a.reconciler.FlushReceipts(callContext); err != nil {
			return fmt.Errorf("retry apply receipts: %w", err)
		}
		desired, err := a.client.Desired(callContext, a.nodeSecret)
		if err != nil {
			return err
		}
		if desired == nil {
			failedKey = ""
			return nil
		}
		key := fmt.Sprintf("%d/%s", desired.Generation, strings.ToLower(strings.TrimSpace(desired.ConfigSHA256)))
		if key == failedKey {
			return nil
		}
		if err := a.applyWithUsage(callContext, *desired); err != nil {
			if !errors.Is(err, errUsageBeforeApply) {
				failedKey = key
			}
			return err
		}
		failedKey = ""
		return nil
	}, func(err error) {
		a.logger.Warn("desired configuration sync failed", "error", err)
	})
}

func runPeriodic(ctx context.Context, interval time.Duration, backoff *reconciler.Backoff, operation func(context.Context) error, report func(error)) error {
	for {
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
		if err := waitContext(ctx, interval); err != nil {
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
