package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/hongle/hl-panel/internal/agent/config"
	agentruntime "github.com/hongle/hl-panel/internal/agent/runtime"
	"github.com/hongle/hl-panel/internal/agent/usage"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func main() {
	configPath := flag.String("config", "config.json", "path to the edge-agent JSON configuration")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Error("load edge-agent configuration", "error", err)
		os.Exit(2)
	}
	statsSources := make(map[agentv1.Engine]usage.CounterSource)
	var closers []interface{ Close() error }
	// Accounting reads the local Xray StatsService independently of process
	// ownership. Xray may be supervised by systemd instead of this Agent; the
	// loopback-only address is validated by config.Load and the stats source.
	if cfg.EngineMode == config.EngineModeXray || cfg.EngineMode == config.EngineModeMixed {
		statsSource, sourceErr := usage.NewXrayStatsSource(cfg.XrayStatsAPIAddress)
		err = sourceErr
		if err != nil {
			logger.Error("initialize Xray StatsService source", "error", err)
			os.Exit(2)
		}
		statsSources[agentv1.EngineXray] = statsSource
		closers = append(closers, statsSource)
	}
	if cfg.EngineMode == config.EngineModeGOST || cfg.EngineMode == config.EngineModeMixed {
		statsSource, sourceErr := usage.NewGOSTStatsSource(cfg.GOSTStatsAPIAddress)
		err = sourceErr
		if err != nil {
			for _, closer := range closers {
				_ = closer.Close()
			}
			logger.Error("initialize GOST stats source", "error", err)
			os.Exit(2)
		}
		statsSources[agentv1.EngineGOST] = statsSource
		closers = append(closers, statsSource)
	}
	agent, err := agentruntime.NewWithOptions(cfg, logger, agentruntime.Options{EngineUsageSources: statsSources})
	if err != nil {
		for _, closer := range closers {
			_ = closer.Close()
		}
		logger.Error("initialize edge-agent", "error", err)
		os.Exit(2)
	}
	defer func() {
		for _, closer := range closers {
			if closeErr := closer.Close(); closeErr != nil {
				logger.Error("close stats source", "error", closeErr)
			}
		}
	}()
	defer func() {
		if closeErr := agent.Close(context.Background()); closeErr != nil {
			logger.Error("close edge-agent engine", "error", closeErr)
		}
	}()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := agent.Run(ctx); err != nil && ctx.Err() == nil {
		logger.Error("edge-agent stopped", "error", err)
		os.Exit(1)
	}
	if ctx.Err() != nil {
		fmt.Fprintln(os.Stderr, "edge-agent stopped")
	}
}
