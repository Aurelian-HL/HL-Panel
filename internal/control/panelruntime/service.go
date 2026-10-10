package panelruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hongle/hl-panel/internal/agent/hostprobe"
	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/faults"
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
	controlMu   sync.Mutex
	legacySeq   atomic.Uint64
	lastControl ControlResult
	logs        *LogStore
	auditor     AuditRecorder
	statePath   string
	controlKey  string
	controlHash string
}

// AuditRecorder is deliberately small so the runtime package does not depend
// on a concrete storage implementation.
type AuditRecorder interface {
	Record(context.Context, audit.Event) error
}

func (s *Service) WithLogs(logs *LogStore) *Service          { s.logs = logs; return s }
func (s *Service) WithAudit(recorder AuditRecorder) *Service { s.auditor = recorder; return s }

// WithStatePath makes the last panel command survive a process restart. The
// file contains only a request digest and a public result, never credentials.
func (s *Service) WithStatePath(path string) *Service {
	s.statePath = strings.TrimSpace(path)
	if s.statePath == "" {
		return s
	}
	if raw, err := readControlState(s.statePath); err == nil {
		var saved struct {
			Key, Hash string
			Result    ControlResult
		}
		if json.Unmarshal(raw, &saved) == nil && saved.Result.CommandID != "" {
			s.lastControl = saved.Result
			s.controlKey, s.controlHash = saved.Key, saved.Hash
		}
	}
	return s
}
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
	key := ""
	if command == "stop" || command == "restart" {
		key = fmt.Sprintf("legacy-%d-%d", s.now().UnixNano(), s.legacySeq.Add(1))
	}
	result, _ := s.control(ctx, command, key, "")
	return result
}

// ControlWithIdempotency is used by HTTP callers for state-changing commands.
// A retry with the same key returns the exact persisted result; reusing a key
// for another command is rejected.
func (s *Service) ControlWithIdempotency(ctx context.Context, command, idempotencyKey string) (ControlResult, error) {
	return s.control(ctx, command, idempotencyKey, "")
}

// ControlWithIdempotencyAs records the authenticated administrator in the
// audit event while preserving the original API for internal callers.
func (s *Service) ControlWithIdempotencyAs(ctx context.Context, command, idempotencyKey, administratorID string) (ControlResult, error) {
	return s.control(ctx, command, idempotencyKey, administratorID)
}

func (s *Service) control(ctx context.Context, command, idempotencyKey, administratorID string) (ControlResult, error) {
	// Reserve the whole operation, including the systemd call. Checking the
	// persisted key and executing outside this lock would allow concurrent
	// retries to run stop/restart twice.
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	command = strings.TrimSpace(command)
	if command == "stop" || command == "restart" {
		if !validIdempotencyKey(idempotencyKey) {
			return ControlResult{Command: command, Status: "failed", Message: "停止或重启必须提供 Idempotency-Key", UpdatedAt: s.now().UTC()}, fmt.Errorf("%w: Idempotency-Key is required", faults.ErrValidation)
		}
	}
	requestHash := controlRequestHash(command)
	s.mu.Lock()
	if idempotencyKey != "" && s.controlKey == idempotencyKey {
		if s.controlHash != requestHash {
			s.mu.Unlock()
			return ControlResult{}, faults.ErrIdempotencyConflict
		}
		result := s.lastControl
		s.mu.Unlock()
		return result, nil
	}
	s.mu.Unlock()
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
	if (command == "stop" || command == "restart") && s.auditor != nil {
		outcome := result.Status
		event, err := audit.NewEvent(now, "administrator", administratorID, "panel."+command, "panel", "control-api", outcome, map[string]any{"command": command})
		if err == nil {
			if err = s.auditor.Record(ctx, event); err != nil {
				s.logger.Warn("panel control audit failed", "command", command, "error", err)
			}
		}
	}
	s.mu.Lock()
	s.lastControl = result
	s.controlKey, s.controlHash = idempotencyKey, requestHash
	s.persistControlLocked()
	s.mu.Unlock()
	return result, nil
}

func readControlState(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 65536 || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return nil, fmt.Errorf("invalid panel control state")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("panel control state changed")
	}
	raw, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil || len(raw) > 65536 {
		return nil, fmt.Errorf("invalid panel control state")
	}
	return raw, nil
}

func controlRequestHash(command string) string {
	sum := sha256.Sum256([]byte("panel.control\x00" + command))
	return hex.EncodeToString(sum[:])
}
func validIdempotencyKey(value string) bool {
	return value != "" && len(value) <= 128 && !strings.ContainsAny(value, " \t\r\n")
}

func (s *Service) persistControlLocked() {
	if s.statePath == "" || s.lastControl.CommandID == "" {
		return
	}
	directory := filepath.Dir(s.statePath)
	if err := os.MkdirAll(directory, 0700); err != nil {
		s.logger.Warn("panel control state directory failed", "error", err)
		return
	}
	payload, err := json.Marshal(struct {
		Key, Hash string
		Result    ControlResult
	}{s.controlKey, s.controlHash, s.lastControl})
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(directory, ".panel-control-*")
	if err != nil {
		s.logger.Warn("panel control state write failed", "error", err)
		return
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(payload)
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(name, s.statePath)
	}
	if err != nil {
		s.logger.Warn("panel control state persist failed", "error", err)
	}
}

func (s *Service) LastControl(context.Context) (ControlResult, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.lastControl.CommandID == "" {
		return ControlResult{}, false
	}
	return s.lastControl, true
}
