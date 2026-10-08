// Package panelupdate authorizes fixed-version, data-preserving panel upgrades.
// The API remains unprivileged; only a local Unix socket reaches the updater.
package panelupdate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/releases"
)

const SocketPath = "/run/hl-panel-update.sock"

var taskID = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)

type Task struct {
	ID              string `json:"id"`
	TargetVersion   string `json:"target_version"`
	State           string `json:"state"`
	Phase           string `json:"phase"`
	Message         string `json:"message"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
	BackupDirectory string `json:"backup_directory,omitempty"`
}

type Status struct {
	Available bool   `json:"available"`
	Message   string `json:"message,omitempty"`
	Task      *Task  `json:"task"`
}

type Service struct {
	auth     *auth.Service
	audit    *audit.Service
	releases versionChecker
	client   *http.Client
}

type versionChecker interface {
	Refresh(context.Context) releases.Status
}

func New(authService *auth.Service, auditService *audit.Service, versions versionChecker) *Service {
	return &Service{auth: authService, audit: auditService, releases: versions, client: &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", SocketPath)
		}},
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (s *Service) Status(ctx context.Context) Status {
	status, err := s.request(ctx, http.MethodGet, "/status", nil)
	if err != nil {
		return Status{Message: "网页更新服务不可用。旧安装需先在面板机运行一次官方终端更新命令；已升级的安装请检查 hl-panel-update.socket。"}
	}
	return status
}

func (s *Service) Start(ctx context.Context, adminID, password, id, version string) (Status, error) {
	if !taskID.MatchString(id) {
		return Status{}, fmt.Errorf("%w: 更新任务标识无效", faults.ErrValidation)
	}
	if _, err := s.auth.ConfirmAdministratorPassword(ctx, adminID, password); err != nil {
		return Status{}, err
	}
	// Permit an authenticated retry after the API restarts into the target version.
	current := s.Status(ctx)
	if current.Task != nil && current.Task.ID == id {
		if current.Task.TargetVersion != version {
			return Status{}, faults.ErrIdempotencyConflict
		}
		return current, nil
	}
	if !current.Available {
		return Status{}, fmt.Errorf("%w: %s", faults.ErrConflict, current.Message)
	}
	if current.Task != nil && current.Task.State == "running" {
		return Status{}, fmt.Errorf("%w: 已有更新任务运行，请查看当前进度", faults.ErrConflict)
	}
	versions := s.releases.Refresh(ctx)
	allowed := false
	for _, candidate := range versions.Versions {
		if candidate.Tag == version && candidate.CanUpdate {
			allowed = true
		}
	}
	if !allowed {
		return Status{}, fmt.Errorf("%w: 请刷新并选择比当前版本更新的 GitHub 正式版本；网络失败时不能提交升级", faults.ErrValidation)
	}
	event, err := audit.NewEvent(time.Now().UTC(), "administrator", adminID, "panel.update.requested", "panel", id, "requested", map[string]any{"target_version": version})
	if err != nil {
		return Status{}, err
	}
	if err = s.audit.Record(ctx, event); err != nil {
		return Status{}, err
	}
	return s.request(ctx, http.MethodPost, "/start", map[string]string{"id": id, "version": version})
}

func (s *Service) request(ctx context.Context, method, path string, input any) (Status, error) {
	var body []byte
	if input != nil {
		body, _ = json.Marshal(input)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://localhost"+path, bytes.NewReader(body))
	if err != nil {
		return Status{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := s.client.Do(req)
	if err != nil {
		return Status{}, fmt.Errorf("%w: 无法连接网页更新服务，请使用终端更新方式或检查 hl-panel-update.socket", faults.ErrConflict)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 16385))
	if err != nil || len(raw) > 16384 {
		return Status{}, fmt.Errorf("%w: 更新服务响应无效", faults.ErrConflict)
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusAccepted {
		var problem struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(raw, &problem) != nil || problem.Message == "" {
			problem.Message = "更新服务拒绝请求"
		}
		return Status{}, fmt.Errorf("%w: %s", faults.ErrConflict, problem.Message)
	}
	var status Status
	if json.Unmarshal(raw, &status) != nil {
		return Status{}, fmt.Errorf("%w: 更新服务响应无效", faults.ErrConflict)
	}
	return status, nil
}
