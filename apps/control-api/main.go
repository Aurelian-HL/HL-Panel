package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/hongle/hl-panel/internal/control/announcements"
	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/customerapi"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/deploymentreceipts"
	"github.com/hongle/hl-panel/internal/control/endpoints"
	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/gatewaymembership"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/groupconfig"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/hostgeo"
	"github.com/hongle/hl-panel/internal/control/httpapi"
	"github.com/hongle/hl-panel/internal/control/migrationbackup"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/control/panelmigration"
	"github.com/hongle/hl-panel/internal/control/panelruntime"
	"github.com/hongle/hl-panel/internal/control/panelupdate"
	"github.com/hongle/hl-panel/internal/control/postgressnapshot"
	"github.com/hongle/hl-panel/internal/control/releases"
	"github.com/hongle/hl-panel/internal/control/rulegroups"
	"github.com/hongle/hl-panel/internal/control/siteconfig"
	"github.com/hongle/hl-panel/internal/control/subscriptions"
	"github.com/hongle/hl-panel/internal/control/targetprobe"
	"github.com/hongle/hl-panel/internal/control/usage"
	"github.com/hongle/hl-panel/internal/control/vlessconnection"
	"github.com/hongle/hl-panel/internal/control/vlessidentity"
	"github.com/hongle/hl-panel/internal/control/vlessruntime"
	"github.com/hongle/hl-panel/internal/idgen"
)

var platformVersion = "development"
var platformBuildTime string
var errMigrationReload = errors.New("reload migrated panel state")

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if len(os.Args) > 1 {
		if os.Args[1] == "reset-admin-password" && len(os.Args) == 3 {
			if err := resetAdministratorPassword(os.Args[2], os.Stdin); err != nil {
				logger.Error("administrator password reset failed", "error", err)
				os.Exit(1)
			}
			logger.Info("administrator password reset; all existing sessions revoked")
			return
		}
		if os.Args[1] != "hash-password" || len(os.Args) != 2 {
			logger.Error("usage: control-api [hash-password | reset-admin-password USERNAME]")
			os.Exit(2)
		}
		if err := hashPasswordFromReader(os.Stdin, os.Stdout); err != nil {
			logger.Error("password hashing failed", "error", err)
			os.Exit(1)
		}
		return
	}
	// Privileged CLI tools must not change the panel service's log ownership.
	logPath := strings.TrimSpace(os.Getenv("CONTROL_PANEL_LOG_FILE"))
	if cwd, _ := os.Getwd(); logPath == "" && cwd == "/var/lib/hl-panel" {
		logPath = "/var/lib/hl-panel/panel.log"
	}
	panelLogs := panelruntime.NewLogStore(logPath)
	logger = slog.New(slog.NewJSONHandler(io.MultiWriter(os.Stdout, panelLogs), nil))
	for {
		err := run(logger, panelLogs)
		if errors.Is(err, errMigrationReload) {
			continue
		}
		if err != nil {
			logger.Error("control api stopped", "error", err)
			os.Exit(1)
		}
		return
	}
}

func hashPasswordFromReader(reader io.Reader, writer io.Writer) error {
	buffered := bufio.NewReader(io.LimitReader(reader, 4097))
	password, err := buffered.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read password from stdin: %w", err)
	}
	password = strings.TrimSuffix(password, "\n")
	password = strings.TrimSuffix(password, "\r")
	if len(password) > 4096 {
		return fmt.Errorf("password exceeds 4096 bytes")
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(writer, string(hash)); err != nil {
		return fmt.Errorf("write password hash: %w", err)
	}
	return nil
}

func run(logger *slog.Logger, panelLogs *panelruntime.LogStore) error {
	configuration, err := loadRuntimeConfig()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	administratorID, err := idgen.New("adm")
	if err != nil {
		return err
	}
	store, closeStore, err := openRepository(context.Background(), configuration, auth.Administrator{
		ID:                 administratorID,
		Username:           configuration.AdministratorUsername,
		PasswordHash:       configuration.AdministratorPasswordHash,
		CreatedAt:          now,
		MustChangePassword: auth.IsDefaultPasswordHash(configuration.AdministratorPasswordHash),
	})
	if err != nil {
		return err
	}
	defer closeStore()
	if durable, ok := store.(*postgressnapshot.Store); ok {
		migrated, exists, err := durable.MigrationRuntime(context.Background())
		if err != nil {
			return err
		}
		if exists {
			configuration.CustomerPasswordFingerprintKey = migrated.PasswordFingerprintKey
			configuration.GatewayPoolTokens = migrated.GatewayPoolTokens
			panelLogs.MergeMigration(migrated.Logs)
		}
	}
	reloadRequested := make(chan struct{}, 1)
	var migrationPending atomic.Bool
	usageRepository, closeUsageRepository, err := openUsageRepository(context.Background(), configuration)
	if err != nil {
		return err
	}
	defer closeUsageRepository()
	auditService := audit.NewService(store)
	authService := auth.NewService(store, auditService, time.Now, 8*time.Hour)
	var enrollmentOptions []enrollment.Option
	if configuration.Nezha != nil {
		configuration.Nezha.SetBindingSource(store)
		enrollmentOptions = append(enrollmentOptions, enrollment.WithNezhaServerValidator(configuration.Nezha.ValidateAvailableServer))
	}
	enrollmentService := enrollment.NewService(store, time.Now, 24*time.Hour, enrollmentOptions...)
	nodeService := nodes.NewService(store, time.Now, 90*time.Second)
	groupService := groups.NewService(store, time.Now)
	gatewayMembershipService := gatewaymembership.NewService(store)
	protocolRunner := gatewaymembership.NewProtocolRunner(store, configuration.ProtocolProbeXrayBinary, configuration.ProtocolProbeEchoPort, configuration.ProtocolProbeInterval, logger)
	endpointService := endpoints.NewServiceWithReadyCandidateSource(store, time.Now, gatewayMembershipService)
	generationService := generations.NewService(store, time.Now, generations.WithDeploymentNotification(protocolRunner.NotifyDeployment))
	customerService := customers.NewService(store, time.Now)
	forwardingService := forwarding.NewService(store, time.Now, forwarding.WithRealityDefaults(configuration.RealityDefaults), forwarding.WithProtocolProbeEchoPort(configuration.ProtocolProbeEchoPort))
	vlessIdentityService, err := vlessidentity.NewService(store, time.Now, nil)
	if err != nil {
		return fmt.Errorf("configure VLESS identity service: %w", err)
	}
	vlessRuntimeService, err := vlessruntime.NewService(store, time.Now)
	if err != nil {
		return fmt.Errorf("configure VLESS runtime service: %w", err)
	}
	usageService := usage.NewService(
		usageRepository,
		usage.NewCustomerPolicyAdapter(store),
		time.Now,
		usage.WithRuleTrafficRepository(store),
		usage.WithLimitPolicyProvider(usage.NewRuntimeLimitPolicyAdapter(store, store)),
		usage.WithLegacyRuleMetadataProvider(usage.NewLegacyRuleMetadataProvider(deploymentreceipts.Repository(store))),
	)
	shutdownContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var migrationGate sync.RWMutex
	fence, err := panelmigration.OpenFence("automatic-migration")
	if err != nil {
		return fmt.Errorf("open migration isolation: %w", err)
	}
	var backgroundMu sync.Mutex
	var background sync.WaitGroup
	var cancelBackground context.CancelFunc
	startBackground := func() {
		backgroundMu.Lock()
		defer backgroundMu.Unlock()
		if fence.Active() || cancelBackground != nil || shutdownContext.Err() != nil {
			return
		}
		ctx, cancel := context.WithCancel(shutdownContext)
		cancelBackground = cancel
		background.Add(2)
		go func() {
			defer background.Done()
			_ = usageService.RunRuleTraffic(ctx, logger)
		}()
		go func() {
			defer background.Done()
			if err := protocolRunner.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				logger.Warn("VLESS protocol observer stopped", "error", err)
			}
		}()
	}
	stopBackground := func() {
		backgroundMu.Lock()
		defer backgroundMu.Unlock()
		if cancelBackground != nil {
			cancelBackground()
			background.Wait()
			cancelBackground = nil
		}
	}
	defer stopBackground()
	versionService := releases.New(platformVersion)
	options := []httpapi.Option{
		httpapi.WithReleases(versionService),
		httpapi.WithPanelUpdate(panelupdate.New(authService, auditService, versionService)),
		httpapi.WithBusiness(customerService, forwardingService, groupconfig.NewService(store, time.Now)),
		httpapi.WithRuleGroups(rulegroups.NewService(store, time.Now)),
		httpapi.WithSite(siteconfig.NewService(store, time.Now), announcements.NewService(store, time.Now), httpapi.PlatformInfo{Version: platformVersion, BuildTime: platformBuildTime}),
		httpapi.WithPanelRuntime(panelruntime.NewService(platformVersion, nil, logger, time.Now).WithLogs(panelLogs)),
		httpapi.WithVLESS(vlessIdentityService, vlessRuntimeService),
		httpapi.WithSubscriptions(subscriptions.NewService(store, vlessconnection.NewService(vlessIdentityService, forwardingService, endpointService), time.Now)),
		httpapi.WithUsage(usageService),
		httpapi.WithNezha(configuration.Nezha),
		httpapi.WithHostGeo(hostgeo.New()),
		httpapi.WithTargetProbe(targetprobe.New(targetprobe.DefaultTimeout)),
	}
	if durable, ok := store.(*postgressnapshot.Store); ok {
		directory := strings.TrimSpace(os.Getenv("CONTROL_MIGRATION_BACKUP_DIR"))
		if directory == "" {
			directory = "migration-backups"
		}
		backupService := migrationbackup.New(durable, authService, auditService, platformVersion, directory, func() migrationbackup.RuntimeSecrets {
			return migrationbackup.RuntimeSecrets{PasswordFingerprintKey: configuration.CustomerPasswordFingerprintKey, GatewayPoolTokens: configuration.GatewayPoolTokens, Logs: panelLogs.Read(2000).Items}
		}, func() {
			migrationPending.Store(true)
			time.AfterFunc(750*time.Millisecond, func() { reloadRequested <- struct{}{} })
		}).WithRestoreGuard(func() func() {
			stopBackground()
			return func() {
				if !migrationPending.Load() && shutdownContext.Err() == nil {
					startBackground()
				}
			}
		})
		domain, ipURL, installer := panelmigration.InstalledConfig()
		automaticService, err := panelmigration.New(authService, auditService, backupService, panelmigration.Config{
			Directory: "automatic-migration", Domain: domain, SourceIPURL: ipURL, Version: platformVersion, Installer: installer,
			Context: shutdownContext, Fence: fence,
			Freeze: func(id string) error {
				migrationGate.Lock()
				defer migrationGate.Unlock()
				if err := fence.Set(panelmigration.FenceState{Role: "source", ID: id}); err != nil {
					return err
				}
				stopBackground()
				return nil
			},
			Thaw: func(id string) error {
				migrationGate.Lock()
				defer migrationGate.Unlock()
				if err := fence.Clear(id); err != nil {
					return err
				}
				startBackground()
				return nil
			},
		})
		if err != nil {
			return fmt.Errorf("configure automatic migration: %w", err)
		}
		defer automaticService.Close()
		options = append(options, httpapi.WithMigrationBackup(backupService), httpapi.WithAutomaticMigration(automaticService))
	}
	if len(configuration.GatewayPoolTokens) > 0 {
		gatewayHandler, err := gatewaymembership.NewHandler(gatewayMembershipService, configuration.GatewayPoolTokens)
		if err != nil {
			return fmt.Errorf("configure gateway membership: %w", err)
		}
		options = append(options, httpapi.WithGatewayMembership(gatewayHandler))
	}
	controlHandler := httpapi.New(authService, enrollmentService, nodeService, groupService, endpointService, generationService, logger, options...)
	customerHandler, err := newCustomerAPIHandler(store, platformVersion, configuration.CustomerPasswordFingerprintKey, logger, customerapi.WithRuleWrites(forwardingService, rulegroups.NewService(store, time.Now)))
	if err != nil {
		return fmt.Errorf("configure customer API: %w", err)
	}
	handler := mountCustomerRoutes(controlHandler, customerHandler)
	guardedHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/panel/migration/import" {
			migrationGate.Lock()
			defer migrationGate.Unlock()
		} else {
			migrationGate.RLock()
			defer migrationGate.RUnlock()
		}
		if r.URL.Path == "/healthz" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "version": platformVersion, "migration_isolated": fence.Active()})
			return
		}
		if !fence.Allows(r) {
			w.Header().Set("Retry-After", "15")
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]string{"message": "面板正在迁移，业务已隔离。请打开迁移备份查看进度；节点会保留现有规则并重试连接。"})
			return
		}
		if migrationPending.Load() {
			w.Header().Set("Retry-After", "2")
			http.Error(w, "面板正在恢复，请稍后重新登录", http.StatusServiceUnavailable)
			return
		}
		handler.ServeHTTP(w, r)
	})
	server := &http.Server{
		Addr:              configuration.ListenAddress,
		Handler:           guardedHandler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       120 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serverErrors := make(chan error, 1)
	startBackground()
	go func() {
		if configuration.DatabaseURL == "" {
			logger.Warn("using volatile in-memory repository; data is lost on restart")
		} else {
			logger.Info("using PostgreSQL transactional snapshot storage", "deployment_scope", "single-instance trial")
		}
		logger.Info("control api listening", "address", configuration.ListenAddress, "tls", configuration.TLSCertificateFile != "")
		if configuration.TLSCertificateFile != "" {
			serverErrors <- server.ListenAndServeTLS(configuration.TLSCertificateFile, configuration.TLSPrivateKeyFile)
			return
		}
		serverErrors <- server.ListenAndServe()
	}()

	select {
	case <-reloadRequested:
		stop()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			return fmt.Errorf("migration reload shutdown failed: %w", err)
		}
		return errMigrationReload
	case <-shutdownContext.Done():
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			return fmt.Errorf("graceful shutdown: %w", err)
		}
		return nil
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
