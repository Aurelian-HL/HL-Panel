package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/hongle/hl-panel/internal/gateway/endpointselector"
	"github.com/hongle/hl-panel/internal/gateway/healthprobe"
	"github.com/hongle/hl-panel/internal/gateway/membership"
	gatewayruntime "github.com/hongle/hl-panel/internal/gateway/runtime"
	"github.com/hongle/hl-panel/internal/gateway/tcpproxy"
	"github.com/hongle/hl-panel/internal/routing/endpointrouter"
)

func main() {
	configPath := flag.String("config", "config.json", "path to the connection-gateway JSON configuration")
	flag.Parse()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, *configPath, logger); err != nil {
		logger.Error("connection gateway stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, configPath string, logger *slog.Logger) error {
	configuration, err := loadRuntimeConfig(configPath)
	if err != nil {
		return err
	}
	var source gatewayruntime.Source
	if configuration.MembershipFile != "" {
		source, err = membership.NewFileSource(configuration.MembershipFile)
	} else {
		source, err = membership.NewHTTPSource(configuration.MembershipURL, configuration.MembershipTokenFile)
	}
	if err != nil {
		return err
	}
	router, err := endpointrouter.New(configuration.SelectionPolicy)
	if err != nil {
		return err
	}
	var runtimeRouter gatewayruntime.Router = router
	var reachabilityMonitor gatewayruntime.ReachabilityMonitor
	if configuration.TCPReachability.Enabled {
		controller, monitorErr := healthprobe.NewController(router, healthprobe.Config{
			Interval:         configuration.TCPReachability.Interval,
			Timeout:          configuration.TCPReachability.Timeout,
			FailureThreshold: configuration.TCPReachability.FailureThreshold,
			SuccessThreshold: configuration.TCPReachability.SuccessThreshold,
			MaxConcurrency:   configuration.TCPReachability.MaxConcurrency,
		}, func(verdict healthprobe.Verdict, reachabilityErr error) {
			if reachabilityErr != nil {
				logger.Debug("discarding stale TCP reachability verdict", "endpoint_id", verdict.ID, "error", reachabilityErr)
				return
			}
			logger.Info("local TCP reachability changed", "endpoint_id", verdict.ID, "reachable", verdict.Reachable, "consecutive", verdict.Consecutive)
		})
		if monitorErr != nil {
			return fmt.Errorf("configure TCP reachability monitor: %w", monitorErr)
		}
		runtimeRouter = controller
		reachabilityMonitor = controller
	}
	selector, err := endpointselector.NewRouterSelector(router)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", configuration.ListenAddress)
	if err != nil {
		return fmt.Errorf("listen on gateway address: %w", err)
	}
	proxy, err := tcpproxy.New(tcpproxy.Config{
		Listener: listener, Selector: selector, Logger: logger, DialTimeout: configuration.DialTimeout,
	})
	if err != nil {
		_ = listener.Close()
		return err
	}
	defer proxy.Close()
	runtime, err := gatewayruntime.New(gatewayruntime.Config{
		Source: source, Router: runtimeRouter, Server: proxy, ReachabilityMonitor: reachabilityMonitor,
		RefreshInterval: configuration.MembershipRefreshInterval, Logger: logger,
	})
	if err != nil {
		_ = proxy.Close()
		return err
	}
	logger.Info("connection gateway listening", "address", proxy.Addr().String(), "policy", configuration.SelectionPolicy,
		"tcp_reachability_enabled", configuration.TCPReachability.Enabled)
	return runtime.Run(ctx)
}
