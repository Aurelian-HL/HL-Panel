package panelruntime

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/hongle/hl-panel/internal/agent/hostprobe"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

type Controller interface {
	Stop(context.Context) error
	Restart(context.Context) error
}

type Runtime struct {
	Status    string                `json:"status"`
	Version   string                `json:"version"`
	StartedAt time.Time             `json:"started_at"`
	Resources *agentv1.HostSnapshot `json:"resources"`
	Log       string                `json:"log"`
}

type ControlResult struct {
	CommandID string    `json:"command_id"`
	Command   string    `json:"command"`
	Status    string    `json:"status"`
	Message   string    `json:"message"`
	Logs      string    `json:"logs"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Service struct {
	version     string
	startedAt   time.Time
	now         func() time.Time
	controller  Controller
	logger      *slog.Logger
	mu          sync.RWMutex
	lastControl ControlResult
	logs        *LogStore
}

func (s *Service) WithLogs(logs *LogStore) *Service { s.logs = logs; return s }
func (s *Service) Logs(limit int) LogResult {
	if s.logs == nil {
		return LogResult{Items: []LogEntry{}, UpdatedAt: s.now().UTC()}
	}
	return s.logs.Read(limit)
}

func NewService(version string, controller Controller, logger *slog.Logger, now func() time.Time) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	if now == nil {
		now = time.Now
	}
	return &Service{version: version, startedAt: now().UTC(), now: now, controller: controller, logger: logger}
}

func (s *Service) Runtime(context.Context) Runtime {
	snapshot := hostprobe.Snapshot()
	return Runtime{
		Status: "online", Version: s.version, StartedAt: s.startedAt,
		Resources: snapshot, Log: fmt.Sprintf("HL-Panel 运行中，服务已启动 %s", s.startedAt.Format(time.RFC3339)),
	}
}

func (s *Service) Control(ctx context.Context, command string) ControlResult {
	now := s.now().UTC()
	result := ControlResult{CommandID: fmt.Sprintf("panel_control_%d", now.UnixNano()), Command: command, Status: "succeeded", UpdatedAt: now}
	switch command {
	case "status":
		result.Message = "面板服务运行中"
	case "version":
		result.Message = "HL-Panel " + s.version
	case "logs":
		result.Message = "面板运行日志"
		for _, entry := range s.Logs(100).Items {
			result.Logs += entry.Time + " " + entry.Level + " " + entry.Message + "\n"
		}
	case "stop", "restart":
		if s.controller == nil {
			result.Status = "failed"
			result.Message = "面板服务未配置受限控制器，无法执行该操作"
			break
		}
		var err error
		if command == "stop" {
			err = s.controller.Stop(ctx)
		} else {
			err = s.controller.Restart(ctx)
		}
		if err != nil {
			result.Status = "failed"
			result.Message = "面板服务操作失败"
			s.logger.Warn("panel control failed", "command", command, "error", err)
		} else if command == "stop" {
			result.Message = "面板服务停止请求已提交"
		} else {
			result.Message = "面板服务重启请求已提交"
		}
	default:
		result.Status = "failed"
		result.Message = "不支持的面板控制命令"
	}
	s.mu.Lock()
	s.lastControl = result
	s.mu.Unlock()
	return result
}

func (s *Service) LastControl(context.Context) (ControlResult, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.lastControl.CommandID == "" {
		return ControlResult{}, false
	}
	return s.lastControl, true
}
