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
	"github.com/hongle/hl-panel/internal/control/licensing"
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
	"github.com/hongle/hl-panel/internal/control/vlesssetup"
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
	enrollmentOptions = append(enrollmentOptions, enrollment.WithIdempotencySecret(configuration.CustomerPasswordFingerprintKey))
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
	licensingService := licensing.NewService(store, time.Now)
	var remoteLicenseIssuer *licensing.RemoteIssuer
	var remoteLicenseLease licensing.Lease
	if configuration.LicenseIssuerURL != "" {
		remoteLicenseIssuer, err = licensing.NewRemoteIssuer(configuration.LicenseIssuerURL, configuration.LicenseHTTPTimeout)
		if err != nil {
			return fmt.Errorf("configure remote license issuer: %w", err)
		}
		remoteLicenseLease, err = bootstrapRemoteLicense(context.Background(), licensingService, remoteLicenseIssuer, configuration, logger)
		if err != nil {
			return err
		}
	}
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
	vlessSetupService := vlesssetup.NewService(groupconfig.NewService(store, time.Now), endpointService, vlessIdentityService)
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
	var licenseBackground sync.WaitGroup
	if remoteLicenseIssuer != nil {
		licenseBackground.Add(1)
		go func() {
			defer licenseBackground.Done()
			runRemoteLicenseRenewal(shutdownContext, licensingService, remoteLicenseIssuer, configuration, remoteLicenseLease, logger)
		}()
	}
	defer licenseBackground.Wait()
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
		background.Add(1)
		go func() {
			defer background.Done()
			runBackgroundSupervisor(ctx, logger, []backgroundTask{
				{name: "VLESS setup reconciliation", run: func(taskContext context.Context) error {
					vlessSetupService.Run(taskContext, forwardingService, logger)
					return nil
				}},
				{name: "rule traffic reconciliation", run: func(taskContext context.Context) error {
					return usageService.RunRuleTraffic(taskContext, logger)
				}},
				{name: "VLESS protocol observer", run: protocolRunner.Run},
			})
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
	controlStatePath := strings.TrimSpace(os.Getenv("CONTROL_PANEL_CONTROL_STATE_FILE"))
	if controlStatePath == "" {
		if cwd, _ := os.Getwd(); cwd == "/var/lib/hl-panel" {
			controlStatePath = "/var/lib/hl-panel/panel-control.json"
		}
	}
	options := []httpapi.Option{
		httpapi.WithReleases(versionService),
		httpapi.WithPanelUpdate(panelupdate.New(authService, auditService, versionService)),
		httpapi.WithLicensing(licensingService),
		httpapi.WithBusiness(customerService, forwardingService, groupconfig.NewService(store, time.Now)),
		httpapi.WithRuleGroups(rulegroups.NewService(store, time.Now)),
		httpapi.WithSite(siteconfig.NewService(store, time.Now), announcements.NewService(store, time.Now), httpapi.PlatformInfo{Version: platformVersion, BuildTime: platformBuildTime}),
		httpapi.WithPanelRuntime(panelruntime.NewService(platformVersion, panelruntime.NewSystemdController("hl-panel-control-api.service"), logger, time.Now).WithLogs(panelLogs).WithAudit(auditService).WithStatePath(controlStatePath)),
		httpapi.WithVLESS(vlessIdentityService, vlessRuntimeService),
		httpapi.WithSubscriptions(subscriptions.NewService(store, vlessconnection.NewService(vlessIdentityService, forwardingService, endpointService, store), time.Now)),
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
			licenseActive := true
			if configuration.LicenseEnforce {
				_, licenseActive, _ = licensingService.CurrentLease(r.Context())
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "version": platformVersion, "migration_isolated": fence.Active(), "license_active": licenseActive})
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
		if configuration.LicenseEnforce && !licenseExempt(r) {
			if _, ok, err := licensingService.CurrentLease(r.Context()); err != nil || !ok {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Cache-Control", "no-store")
				w.WriteHeader(http.StatusForbidden)
				code, message := "license_invalid", "面板未激活授权"
				if errors.Is(err, licensing.ErrExpired) {
					code, message = "license_expired", "面板授权已到期"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": message}})
				return
			}
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

func licenseExempt(r *http.Request) bool {
	if r.URL.Path == "/healthz" || r.URL.Path == "/api/v1/auth/login" {
		return true
	}
	if strings.HasPrefix(r.URL.Path, "/api/v1/public/license/") {
		return true
	}
	if r.URL.Path == "/api/v1/license" || strings.HasPrefix(r.URL.Path, "/api/v1/license/") {
		return true
	}
	return false
}

func bootstrapRemoteLicense(ctx context.Context, service *licensing.Service, issuer *licensing.RemoteIssuer, configuration runtimeConfig, logger *slog.Logger) (licensing.Lease, error) {
	lease, available, err := service.CurrentLease(ctx)
	if err != nil && !errors.Is(err, licensing.ErrExpired) {
		return licensing.Lease{}, fmt.Errorf("read persisted license lease: %w", err)
	}
	if available || hasRemoteLeaseData(lease) {
		if err := validateRemoteLeaseAt(lease, configuration, "", time.Now().UTC()); err != nil {
			return licensing.Lease{}, fmt.Errorf("validate persisted license lease: %w", err)
		}
		renewed, renewErr := renewRemoteLicense(ctx, issuer, lease, configuration)
		if renewErr == nil {
			if err := service.StoreLease(ctx, renewed); err != nil {
				return licensing.Lease{}, fmt.Errorf("store renewed license lease: %w", err)
			}
			return renewed, nil
		}
		if isLicenseConfigurationError(renewErr) {
			return licensing.Lease{}, fmt.Errorf("renew persisted license lease: %w", renewErr)
		}
		logger.Warn("remote license renewal unavailable; keeping current valid lease", "error", renewErr, "expires_at", lease.ExpiresAt)
		return lease, nil
	}
	activated, activateErr := activateRemoteLicense(ctx, issuer, configuration)
	if activateErr != nil {
		return licensing.Lease{}, fmt.Errorf("remote license activation failed: %w", activateErr)
	}
	if err := service.StoreLease(ctx, activated); err != nil {
		return licensing.Lease{}, fmt.Errorf("store activated license lease: %w", err)
	}
	return activated, nil
}

func activateRemoteLicense(ctx context.Context, issuer *licensing.RemoteIssuer, configuration runtimeConfig) (licensing.Lease, error) {
	requestContext, cancel := context.WithTimeout(ctx, configuration.LicenseHTTPTimeout)
	defer cancel()
	lease, err := issuer.Activate(requestContext, configuration.LicenseCode, configuration.LicenseDomain, configuration.LicenseInstanceID)
	if err != nil {
		return licensing.Lease{}, err
	}
	if err := validateRemoteLeaseAt(lease, configuration, "", time.Now().UTC()); err != nil {
		return licensing.Lease{}, err
	}
	return lease, nil
}

func renewRemoteLicense(ctx context.Context, issuer *licensing.RemoteIssuer, lease licensing.Lease, configuration runtimeConfig) (licensing.Lease, error) {
	requestContext, cancel := context.WithTimeout(ctx, configuration.LicenseHTTPTimeout)
	defer cancel()
	renewed, err := issuer.Renew(requestContext, lease.LicenseID, configuration.LicenseCode, configuration.LicenseDomain, configuration.LicenseInstanceID)
	if err != nil {
		return licensing.Lease{}, err
	}
	if err := validateRemoteLeaseAt(renewed, configuration, lease.LicenseID, time.Now().UTC()); err != nil {
		return licensing.Lease{}, err
	}
	return renewed, nil
}

func validateRemoteLease(lease licensing.Lease, configuration runtimeConfig, expectedLicenseID string) error {
	return validateRemoteLeaseAt(lease, configuration, expectedLicenseID, time.Now().UTC())
}

const remoteLeaseClockSkew = 5 * time.Minute

func validateRemoteLeaseAt(lease licensing.Lease, configuration runtimeConfig, expectedLicenseID string, now time.Time) error {
	now = now.UTC()
	if strings.TrimSpace(lease.LicenseID) == "" || strings.TrimSpace(lease.InstanceID) == "" || strings.TrimSpace(lease.Domain) == "" || lease.ActivatedAt.IsZero() || lease.CheckedAt.IsZero() || lease.ExpiresAt.IsZero() {
		return licensing.ErrInvalidCode
	}
	if expectedLicenseID != "" && lease.LicenseID != expectedLicenseID {
		return licensing.ErrBound
	}
	if lease.InstanceID != configuration.LicenseInstanceID {
		return licensing.ErrBound
	}
	if !licensing.DomainMatches(lease.Domain, configuration.LicenseDomain) {
		return licensing.ErrDomainMismatch
	}
	if !lease.ExpiresAt.After(now) || !lease.ExpiresAt.After(lease.ActivatedAt) {
		return licensing.ErrExpired
	}
	if lease.ActivatedAt.After(now.Add(remoteLeaseClockSkew)) || lease.CheckedAt.After(now.Add(remoteLeaseClockSkew)) || lease.CheckedAt.Before(lease.ActivatedAt) {
		return licensing.ErrInvalidCode
	}
	return nil
}

func hasRemoteLeaseData(lease licensing.Lease) bool {
	return lease.LicenseID != "" || lease.InstanceID != "" || lease.Domain != "" || !lease.ActivatedAt.IsZero() || !lease.ExpiresAt.IsZero() || !lease.CheckedAt.IsZero()
}

func runRemoteLicenseRenewal(ctx context.Context, service *licensing.Service, issuer *licensing.RemoteIssuer, configuration runtimeConfig, lease licensing.Lease, logger *slog.Logger) {
	timer := time.NewTimer(licenseRenewalDelay(time.Now().UTC(), lease.ExpiresAt, configuration.LicenseRenewInterval))
	defer timer.Stop()
	failureAttempt := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			renewed, err := renewRemoteLicense(ctx, issuer, lease, configuration)
			if err != nil {
				logger.Warn("remote license renewal failed", "error", err, "expires_at", lease.ExpiresAt)
				if isLicenseConfigurationError(err) {
					return
				}
				failureAttempt++
				timer.Reset(licenseRenewalRetryDelay(time.Now().UTC(), lease.ExpiresAt, failureAttempt))
				continue
			}
			if err := service.StoreLease(ctx, renewed); err != nil {
				logger.Warn("store remote license lease failed", "error", err)
				if isLicenseConfigurationError(err) {
					return
				}
				failureAttempt++
				timer.Reset(licenseRenewalRetryDelay(time.Now().UTC(), lease.ExpiresAt, failureAttempt))
				continue
			}
			lease = renewed
			failureAttempt = 0
			logger.Info("remote license renewed", "expires_at", lease.ExpiresAt)
			timer.Reset(licenseRenewalDelay(time.Now().UTC(), lease.ExpiresAt, configuration.LicenseRenewInterval))
		}
	}
}

func licenseRenewalDelay(now, expiresAt time.Time, interval time.Duration) time.Duration {
	if interval <= 0 {
		interval = time.Minute
	}
	remaining := expiresAt.Sub(now)
	if remaining <= 0 {
		return time.Second
	}
	delay := interval
	if half := remaining / 2; half > 0 && half < delay {
		delay = half
	}
	if delay < time.Second {
		return time.Second
	}
	return delay
}

func licenseRenewalRetryDelay(now, expiresAt time.Time, attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Minute
	for i := 1; i < attempt && delay < 15*time.Minute; i++ {
		delay *= 2
	}
	if delay > 15*time.Minute {
		delay = 15 * time.Minute
	}
	remaining := expiresAt.Sub(now)
	if remaining > 0 && remaining/3 < delay {
		delay = remaining / 3
	}
	if delay < time.Second {
		delay = time.Second
	}
	return delay
}

func isLicenseConfigurationError(err error) bool {
	return errors.Is(err, licensing.ErrInvalidCode) || errors.Is(err, licensing.ErrExpired) || errors.Is(err, licensing.ErrRevoked) || errors.Is(err, licensing.ErrBound) || errors.Is(err, licensing.ErrDomainMismatch)
}
