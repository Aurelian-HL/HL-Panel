package panelmigration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/migrationbackup"
)

var validID = regexp.MustCompile(`^[a-f0-9]{8}(-[a-f0-9]{4}){3}-[a-f0-9]{12}$`)
var recoveryPath = regexp.MustCompile(`^/api/v1/panel/migration/recovery/mbk_[a-f0-9]{32}$`)
var versionPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)
var domainPattern = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

type Input struct {
	ID     string `json:"id"`
	Target Target `json:"target"`
	Credentials
	AdministratorPassword string `json:"administrator_password"`
	Password              string `json:"password"`
	SourceURL             string `json:"source_url"`
	Confirm               string `json:"confirm"`
}
type Task struct {
	ID               string         `json:"id"`
	Target           Target         `json:"target"`
	SourceURL        string         `json:"source_url"`
	Domain           string         `json:"domain"`
	Version          string         `json:"version"`
	State            string         `json:"state"`
	Phase            string         `json:"phase"`
	Message          string         `json:"message"`
	CreatedAt        time.Time      `json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
	BackupID         string         `json:"backup_id"`
	TargetRecoveryID string         `json:"target_recovery_id,omitempty"`
	Digest           string         `json:"digest,omitempty"`
	Counts           map[string]int `json:"counts,omitempty"`
}
type Status struct {
	SourceURL           string `json:"source_url,omitempty"`
	Available           bool   `json:"available"`
	Message             string `json:"message,omitempty"`
	Task                *Task  `json:"task"`
	Frozen              bool   `json:"frozen"`
	CredentialsRequired bool   `json:"credentials_required"`
	SourceIPURL         string `json:"source_ip_url,omitempty"`
	Running             bool   `json:"running"`
}
type Config struct {
	Directory, Domain, SourceIPURL, Version, Installer string
	Context                                            context.Context
	Freeze                                             func(string) error
	Thaw                                               func(string) error
	Fence                                              *Fence
	Transport                                          Transport
}
type secretSession struct {
	input     Input
	adminID   string
	username  string
	bootstrap string
	expires   time.Time
}
type Service struct {
	workers sync.WaitGroup
	cancel  context.CancelFunc
	timer   *time.Timer
	mu      sync.Mutex
	auth    *auth.Service
	audit   *audit.Service
	backup  *migrationbackup.Service
	config  Config
	task    *Task
	secret  *secretSession
	running bool
	closed  bool
}

func New(a *auth.Service, events *audit.Service, backup *migrationbackup.Service, config Config) (*Service, error) {
	if a == nil || events == nil || backup == nil || config.Fence == nil || config.Freeze == nil || config.Thaw == nil {
		return nil, errors.New("incomplete automatic migration configuration")
	}
	if config.Context == nil {
		config.Context = context.Background()
	}
	ctx, cancel := context.WithCancel(config.Context)
	config.Context = ctx
	if config.Transport == nil {
		config.Transport = SSHTransport{}
	}
	s := &Service{auth: a, audit: events, backup: backup, config: config, cancel: cancel}
	raw, err := readPrivate(filepath.Join(config.Directory, "task.json"))
	if errors.Is(err, os.ErrNotExist) {
		raw, err = readPrivate(filepath.Join(config.Directory, "received.json"))
	}
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		cancel()
		return nil, err
	}
	var task Task
	if json.Unmarshal(raw, &task) != nil || !validID.MatchString(task.ID) {
		cancel()
		return nil, errors.New("invalid migration task metadata")
	}
	if task.State != "completed" && task.State != "cancelled" {
		task.State = "failed"
		task.Message = "面板已重新启动。凭据未保存，请重新输入 SSH 凭据和备份密码继续；现有隔离状态已保留。"
	}
	s.task = &task
	return s, nil
}

func (s *Service) Close() {
	s.mu.Lock()
	s.closed = true
	s.cancel()
	s.mu.Unlock()
	s.workers.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.timer != nil {
		s.timer.Stop()
	}
	s.secret = nil
}

func (s *Service) available() bool {
	return domainPattern.MatchString(s.config.Domain) && s.config.Domain != "hl-panel.invalid" && versionPattern.MatchString(s.config.Version) && strings.Contains(s.config.Installer, "--migration-receiver")
}
func (s *Service) statusLocked() Status {
	status := Status{Available: s.available(), Frozen: s.config.Fence.Active(), SourceIPURL: s.config.SourceIPURL, Running: s.running,
		CredentialsRequired: s.secret == nil || time.Now().After(s.secret.expires)}
	if s.config.Domain != "" {
		status.SourceURL = "https://" + s.config.Domain
	}
	if !status.Available {
		status.Message = "自动迁移需要官方正式版本、标准安装环境和已配置的原面板域名。IP 地址迁移请使用导出和导入。"
	}
	if s.task != nil {
		copy := *s.task
		copy.Counts = make(map[string]int, len(s.task.Counts))
		for key, count := range s.task.Counts {
			copy.Counts[key] = count
		}
		status.Task = &copy
	}
	return status
}
func (s *Service) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	// The receiver receipt is written after its process has restarted and passed TLS checks.
	if s.task == nil && !s.config.Fence.Active() {
		if raw, err := readPrivate(filepath.Join(s.config.Directory, "received.json")); err == nil {
			var task Task
			if json.Unmarshal(raw, &task) == nil && validID.MatchString(task.ID) && task.State == "completed" {
				s.task = &task
			}
		}
	}
	return s.statusLocked()
}

func invalid(message string) error  { return fmt.Errorf("%w: %s", faults.ErrValidation, message) }
func conflict(message string) error { return fmt.Errorf("%w: %s", faults.ErrConflict, message) }
func validateTarget(target Target, pin bool) error {
	ip := net.ParseIP(target.Host)
	if ip == nil || ip.To4() == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsUnspecified() {
		return invalid("请填写新服务器的公网 IPv4 地址")
	}
	for _, block := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4"} {
		_, subnet, _ := net.ParseCIDR(block)
		if subnet.Contains(ip) {
			return invalid("新服务器地址必须是可路由的公网 IPv4")
		}
	}
	if target.Port < 1 || target.Port > 65535 {
		return invalid("SSH 端口需为 1 至 65535")
	}
	if pin && !fingerprintPattern.MatchString(target.Fingerprint) {
		return invalid("请先获取并确认新服务器 SSH 指纹")
	}
	return nil
}
func (s *Service) Probe(ctx context.Context, adminID, password string, target Target) (string, error) {
	if _, err := s.auth.ConfirmAdministratorPassword(ctx, adminID, password); err != nil {
		return "", err
	}
	if err := validateTarget(target, false); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return s.config.Transport.Probe(ctx, target)
}
func (s *Service) validate(input Input) error {
	if !s.available() {
		return conflict("当前安装环境不支持面板内自动迁移")
	}
	if !validID.MatchString(input.ID) {
		return invalid("迁移任务编号无效")
	}
	if err := validateTarget(input.Target, true); err != nil {
		return err
	}
	if len(input.Password) < 10 || len(input.Password) > 256 {
		return invalid("备份密码需为 10 至 256 个字符")
	}
	if input.Password == input.AdministratorPassword {
		return invalid("备份密码请与管理员密码分开设置")
	}
	if (input.Credentials.Password == "") == (input.PrivateKey == "") {
		return invalid("请填写一种 SSH 凭据：密码或私钥")
	}
	if len(input.Credentials.Password) > 4096 || len(input.PrivateKey) > 16384 || len(input.Passphrase) > 4096 {
		return invalid("SSH 凭据超过长度限制")
	}
	parsed, err := url.Parse(input.SourceURL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() != s.config.Domain || (parsed.Port() != "" && parsed.Port() != "443") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return invalid("自动迁移须沿用安装时配置的 HTTPS 域名；请填写原面板地址")
	}
	if old, err := url.Parse(s.config.SourceIPURL); err == nil && old.Hostname() == input.Target.Host {
		return invalid("新服务器不能是当前面板机器")
	}
	return nil
}

func (s *Service) event(ctx context.Context, adminID, operation, id string) error {
	outcome := "requested"
	if strings.HasSuffix(operation, ".succeeded") {
		outcome = "succeeded"
	}
	if strings.HasSuffix(operation, ".failed") {
		outcome = "failed"
	}
	event, err := audit.NewEvent(time.Now().UTC(), "administrator", adminID, "panel.migration."+operation, "panel", id, outcome, nil)
	if err != nil {
		return err
	}
	return s.audit.Record(ctx, event)
}
func (s *Service) persistLocked() error {
	s.task.UpdatedAt = time.Now().UTC()
	return writePrivate(filepath.Join(s.config.Directory, "task.json"), s.task)
}
func (s *Service) update(state, phase, message string, mutate func(*Task)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := *s.task
	s.task.State, s.task.Phase, s.task.Message = state, phase, message
	if mutate != nil {
		mutate(s.task)
	}
	if err := s.persistLocked(); err != nil {
		*s.task = previous
		return err
	}
	return nil
}

func (s *Service) Start(ctx context.Context, adminID string, input Input) (Status, error) {
	if _, err := s.auth.ConfirmAdministratorPassword(ctx, adminID, input.AdministratorPassword); err != nil {
		return Status{}, err
	}
	if err := s.validate(input); err != nil {
		return Status{}, err
	}
	user, err := s.auth.CurrentAdministrator(ctx, adminID)
	if err != nil {
		return Status{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Status{}, conflict("面板正在停止，请重新连接后继续")
	}
	previousTask, previousSecret := s.task, s.secret
	if s.task != nil {
		copy := *s.task
		s.task = &copy
	}
	committed := false
	defer func() {
		if !committed {
			s.task, s.secret = previousTask, previousSecret
		}
	}()
	if s.task != nil && s.task.ID == input.ID {
		if s.task.Version != s.config.Version {
			return Status{}, conflict("迁移期间面板版本已改变，请先恢复原任务版本后继续")
		}
		if s.task.Target != input.Target || s.task.SourceURL != strings.TrimSuffix(input.SourceURL, "/") {
			return Status{}, faults.ErrIdempotencyConflict
		}
		if s.running || s.task.State == "completed" || s.task.State == "cancelled" {
			committed = true
			return s.statusLocked(), nil
		}
	} else {
		if s.running || s.config.Fence.Active() || (s.task != nil && s.task.State != "cancelled" && s.task.State != "completed") {
			return Status{}, conflict("已有迁移任务，请继续或撤销后再创建新任务")
		}
		if input.Confirm != "PREPARE" {
			return Status{}, invalid("请确认新机是全新服务器，并确认准备迁移")
		}
		s.task = &Task{ID: input.ID, Target: input.Target, SourceURL: strings.TrimSuffix(input.SourceURL, "/"), Domain: s.config.Domain,
			Version: s.config.Version, State: "running", Phase: "prepare", CreatedAt: time.Now().UTC()}
	}
	if err := s.event(ctx, adminID, "automatic.requested", input.ID); err != nil {
		return Status{}, err
	}
	var random [24]byte
	if _, err := rand.Read(random[:]); err != nil {
		return Status{}, err
	}
	secret := &secretSession{input: input, adminID: adminID, username: user.Username, bootstrap: hex.EncodeToString(random[:]), expires: time.Now().Add(90 * time.Minute)}
	s.secret = secret
	s.task.State = "running"
	s.task.Message = "正在连接新服务器，准备受管安装环境。"
	if err := s.persistLocked(); err != nil {
		s.secret = nil
		return Status{}, err
	}
	s.running = true
	committed = true
	s.workers.Add(1)
	go s.run(secret, false)
	// Credentials are not written to task.json; discard idle sessions as well.
	if s.timer != nil {
		s.timer.Stop()
	}
	expiry := secret.expires
	s.timer = time.AfterFunc(90*time.Minute, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.secret != nil && s.secret.expires == expiry && !s.running {
			s.secret = nil
		}
	})
	return s.statusLocked(), nil
}

func (s *Service) Continue(ctx context.Context, adminID, password, id, confirmation string) (Status, error) {
	if _, err := s.auth.ConfirmAdministratorPassword(ctx, adminID, password); err != nil {
		return Status{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Status{}, conflict("面板正在停止，请重新连接后继续")
	}
	if s.task == nil || s.task.ID != id {
		return Status{}, faults.ErrNotFound
	}
	if s.running || s.task.State == "completed" {
		return s.statusLocked(), nil
	}
	if s.task.State != "ready" {
		return Status{}, conflict("请先重新连接并准备新机")
	}
	if confirmation != "CUTOVER" {
		return Status{}, invalid("请确认正式切换；旧面板将暂停业务写入")
	}
	if s.secret == nil || time.Now().After(s.secret.expires) {
		return Status{}, conflict("SSH 凭据已过期，请重新输入凭据和备份密码连接新机")
	}
	if err := s.event(ctx, adminID, "automatic.cutover", id); err != nil {
		return Status{}, err
	}
	s.secret.input.AdministratorPassword = password
	s.secret.adminID = adminID
	previous := *s.task
	s.task.State, s.task.Phase, s.task.Message = "running", "restore", "正在冻结旧面板并创建最终备份。"
	if err := s.persistLocked(); err != nil {
		*s.task = previous
		return Status{}, err
	}
	s.running = true
	s.workers.Add(1)
	go s.run(s.secret, true)
	return s.statusLocked(), nil
}

func (s *Service) run(secret *secretSession, cutover bool) {
	defer s.workers.Done()
	ctx, cancel := context.WithDeadline(s.config.Context, secret.expires)
	defer cancel()
	err := s.execute(ctx, secret, cutover)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running = false
	if err != nil {
		s.task.State = "failed"
		s.task.Message = "迁移操作未完成，数据和隔离状态已保留，请重新连接后继续。"
		if errors.Is(err, faults.ErrValidation) {
			s.task.Message = err.Error()
		}
		var remoteFailure *remoteFailure
		if errors.As(err, &remoteFailure) {
			s.task.Message = remoteFailure.Error()
		}
		if ctx.Err() != nil {
			s.task.Message = "迁移任务中断或等待超时。凭据已释放，请重新输入后继续；数据和隔离状态已保留。"
		}
		s.secret = nil
		_ = s.persistLocked()
	} else if s.task.State == "completed" {
		s.secret = nil
	}
	if err != nil || s.task.State == "completed" {
		if s.timer != nil {
			s.timer.Stop()
		}
		operation := "automatic.succeeded"
		if err != nil {
			operation = "automatic.failed"
		}
		auditContext, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_ = s.event(auditContext, secret.adminID, operation, s.task.ID)
	}
}
func (s *Service) remote(secret *secretSession, action string) RemoteInput {
	return RemoteInput{Action: action, ID: secret.input.ID, Version: s.config.Version, Domain: s.config.Domain, PublicIP: secret.input.Target.Host,
		SourceURL: secret.input.SourceURL, Username: secret.username, AdministratorPassword: secret.input.AdministratorPassword,
		BootstrapPassword: secret.bootstrap, Password: secret.input.Password}
}
func (s *Service) execute(ctx context.Context, secret *secretSession, cutover bool) error {
	status := s.Status()
	task := status.Task
	if task.BackupID == "" && !cutover && !status.Frozen {
		input := s.remote(secret, "prepare")
		input.Installer = s.config.Installer
		result, err := s.config.Transport.Run(ctx, task.Target, secret.input.Credentials, input)
		if err != nil {
			return err
		}
		if result.State != "prepared" {
			return errors.New("新机安装状态未确认，请重新连接后继续")
		}
		return s.update("ready", "prepare", "新机安装完成，业务仍保持隔离。确认正式切换后，将导出最终数据并恢复；随后把原域名解析指向新机。", nil)
	}
	if err := s.config.Freeze(task.ID); err != nil {
		return errors.New("无法冻结旧面板；尚未继续恢复，请检查隔离状态")
	}
	var raw []byte
	var err error
	if task.BackupID == "" {
		raw, err = s.backup.Export(ctx, secret.adminID, secret.input.AdministratorPassword, secret.input.Password, task.SourceURL)
		if err != nil {
			return errors.New("最终备份导出失败；旧面板保持隔离，可重试或撤销切换")
		}
		preview, err := s.backup.Preview(ctx, secret.adminID, secret.input.AdministratorPassword, secret.input.Password, raw)
		if err != nil {
			return errors.New("最终备份校验失败，已阻止恢复")
		}
		id, err := s.backup.Retain(raw)
		if err != nil {
			return errors.New("无法保存最终备份，已阻止恢复")
		}
		if err := s.update("running", "restore", "最终备份已保存，正在传输并恢复新机数据。", func(t *Task) { t.BackupID, t.Digest, t.Counts = id, preview.Digest, preview.Counts }); err != nil {
			return err
		}
		task = s.Status().Task
	} else {
		raw, err = s.backup.Recovery(task.BackupID)
		if err != nil {
			return errors.New("最终备份无法读取，保持隔离，需人工恢复")
		}
		if _, err := s.backup.Preview(ctx, secret.adminID, secret.input.AdministratorPassword, secret.input.Password, raw); err != nil {
			return invalid("备份密码不正确或备份损坏，请重新输入本次迁移的备份密码")
		}
	}
	defer clear(raw)
	if task.Phase != "certificate" && task.Phase != "completed" {
		input := s.remote(secret, "restore")
		input.Archive, input.Digest, input.Counts = raw, task.Digest, task.Counts
		result, err := s.config.Transport.Run(ctx, task.Target, secret.input.Credentials, input)
		if err != nil {
			return err
		}
		if result.State != "restored" || result.RecoveryID == "" {
			return errors.New("新机恢复记录未确认，保持隔离，请重新连接后继续")
		}
		if err := s.update("waiting_dns", "certificate", "数据恢复完成。请把原域名 A 记录改为新机 IP，并处理旧 AAAA 记录；解析就绪后自动申请证书并启用新面板。", func(t *Task) { t.TargetRecoveryID = result.RecoveryID }); err != nil {
			return err
		}
	}
	for {
		result, err := s.config.Transport.Run(ctx, task.Target, secret.input.Credentials, s.remote(secret, "activate"))
		if err != nil {
			return err
		}
		if result.State == "completed" {
			return s.update("completed", "completed", "新面板恢复和 HTTPS 切换完成。旧机仍保持隔离，请到原域名登录并核对节点；旧机数据和最终备份已保留。", nil)
		}
		if result.State != "waiting_dns" {
			return errors.New("新机返回未知迁移状态，尚未确认完成")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(15 * time.Second):
		}
	}
}

func (s *Service) Rollback(ctx context.Context, adminID string, input Input) (Status, error) {
	if _, err := s.auth.ConfirmAdministratorPassword(ctx, adminID, input.AdministratorPassword); err != nil {
		return Status{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Status{}, conflict("面板正在停止，请重新连接后继续")
	}
	if s.task == nil || input.ID != s.task.ID {
		return Status{}, faults.ErrNotFound
	}
	if s.task.State == "cancelled" {
		return s.statusLocked(), nil
	}
	if s.running || s.task.State == "completed" {
		return Status{}, conflict("运行中或已启用新机的任务不能直接撤销；请先核对新机写入及域名解析")
	}
	if input.Confirm != "ROLLBACK" {
		return Status{}, invalid("请确认域名已指回旧机，再撤销迁移")
	}
	credentials := input.Credentials
	if credentials.Password == "" && credentials.PrivateKey == "" && s.secret != nil {
		credentials = s.secret.input.Credentials
	}
	if credentials.Password == "" && credentials.PrivateKey == "" {
		return Status{}, invalid("请重新填写新机 SSH 凭据，以确认新机已停止")
	}
	if err := s.event(ctx, adminID, "automatic.rollback", input.ID); err != nil {
		return Status{}, err
	}
	s.running = true
	s.workers.Add(1)
	target, id := s.task.Target, s.task.ID
	go func() {
		defer s.workers.Done()
		ctx, cancel := context.WithTimeout(s.config.Context, 3*time.Minute)
		defer cancel()
		result, err := s.config.Transport.Run(ctx, target, credentials, RemoteInput{Action: "rollback", ID: id})
		if err == nil && result.State != "cancelled" {
			err = errors.New("target stop was not confirmed")
		}
		if err == nil {
			err = s.config.Thaw(id)
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.running, s.secret = false, nil
		if err != nil {
			s.task.State, s.task.Message = "failed", "撤销未完成：新机可能已启用或无法确认已停止。旧机仍保持隔离，请核对新机状态。"
		} else {
			s.task.State, s.task.Phase, s.task.Message = "cancelled", "rollback", "新机已停止，旧机已恢复业务写入。两端数据和备份均保留。"
		}
		_ = s.persistLocked()
		operation := "automatic.rollback.succeeded"
		if err != nil {
			operation = "automatic.rollback.failed"
		}
		auditContext, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_ = s.event(auditContext, adminID, operation, id)
	}()
	return s.statusLocked(), nil
}

// InstalledConfig reads only validated nonsecret domain settings, never env credentials.
func InstalledConfig() (domain, ipURL, installer string) {
	raw, _ := os.ReadFile("/etc/hl-panel/domain.conf")
	values := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			values[key] = strings.Trim(value, "'\" \r")
		}
	}
	if domainPattern.MatchString(values["DOMAIN"]) {
		domain = values["DOMAIN"]
	}
	if net.ParseIP(values["PUBLIC_IP"]) != nil {
		port := values["IP_HTTPS_PORT"]
		if port == "" {
			port = "443"
		}
		if regexp.MustCompile(`^[0-9]{1,5}$`).MatchString(port) {
			ipURL = "https://" + values["PUBLIC_IP"] + ":" + port
		}
	}
	if executable, err := os.Executable(); err == nil {
		raw, err := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(executable)), "deploy", "install.sh"))
		if err == nil {
			installer = string(raw)
		}
	}
	return
}
