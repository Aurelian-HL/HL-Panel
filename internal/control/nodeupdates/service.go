// Package nodeupdates owns durable, fixed-purpose node upgrade tasks.
package nodeupdates

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/control/releases"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

const Capability = "node-update-v1"

var IDPattern = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)
var SHA256Pattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type Task struct {
	ID              string     `json:"id"`
	NodeID          string     `json:"node_id"`
	TargetVersion   string     `json:"target_version"`
	ArchiveSHA256   string     `json:"archive_sha256"`
	State           string     `json:"state"`
	Phase           string     `json:"phase"`
	Message         string     `json:"message"`
	BackupDirectory string     `json:"backup_directory,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	ClaimedAt       *time.Time `json:"claimed_at,omitempty"`
}

func (t Task) Active() bool {
	return t.State != "succeeded" && t.State != "failed" && t.State != "rolled_back" && t.State != "cancelled"
}

// Record is private persistence state; never serialize it to public APIs.
type Record struct {
	Task            Task
	AdministratorID string
	Lease           string
}
type Status struct {
	Available      bool     `json:"available"`
	Message        string   `json:"message"`
	CurrentVersion string   `json:"current_version"`
	PanelVersion   string   `json:"panel_version"`
	Versions       []string `json:"versions"`
	Task           *Task    `json:"task"`
}
type Repository interface {
	ListNodes(context.Context) ([]nodes.Node, error)
	ReplayNodeUpdate(context.Context, string, string, string, string) (*Task, error)
	LatestNodeUpdate(context.Context, string) (*Task, error)
	CreateNodeUpdate(context.Context, Record, audit.Event) (Task, error)
	CancelNodeUpdate(context.Context, string, string, string, time.Time) (Task, error)
	ClaimNodeUpdate(context.Context, string, string, time.Time) (*Record, error)
	ReportNodeUpdate(context.Context, string, agentv1.NodeUpdateResult, time.Time) error
}
type Versions interface {
	Check(context.Context) releases.Status
	ResolveNodeArchive(context.Context, string, string) (string, error)
}
type Service struct {
	repo     Repository
	releases Versions
	now      func() time.Time
}

func New(repo Repository, versions Versions) *Service {
	return &Service{repo: repo, releases: versions, now: time.Now}
}
func (s *Service) node(ctx context.Context, id string) (nodes.Node, error) {
	list, err := s.repo.ListNodes(ctx)
	if err != nil {
		return nodes.Node{}, err
	}
	for _, n := range list {
		if n.ID == id && n.DeletedAt == nil {
			return n, nil
		}
	}
	return nodes.Node{}, faults.ErrNotFound
}
func Eligible(n nodes.Node, now time.Time) bool {
	if n.Platform != "linux" || n.Architecture != "amd64" || n.LastHeartbeatAt == nil || now.Sub(*n.LastHeartbeatAt) > 90*time.Second {
		return false
	}
	for _, c := range n.Capabilities {
		if c == Capability {
			return true
		}
	}
	return false
}
func (s *Service) Status(ctx context.Context, id string) (Status, error) {
	n, err := s.node(ctx, id)
	if err != nil {
		return Status{}, err
	}
	task, err := s.repo.LatestNodeUpdate(ctx, id)
	if err != nil {
		return Status{}, err
	}
	status := Status{Available: Eligible(n, s.now()), CurrentVersion: n.AgentVersion, Task: task, Versions: []string{}, Message: "旧节点请先执行一次保身份更新；新节点使用令牌安装即自动接入。离线或不支持的平台不能更新。"}
	versions := s.releases.Check(ctx)
	status.PanelVersion = versions.Current
	for _, v := range versions.Versions {
		if (v.Tag == n.AgentVersion || releases.IsNewer(v.Tag, n.AgentVersion)) && !releases.IsNewer(v.Tag, versions.Current) {
			status.Versions = append(status.Versions, v.Tag)
		}
	}
	if status.Available {
		status.Message = "更新切换会断开此节点已有连接；身份、规则和订阅保持不变。"
	}
	if versions.State == "check_failed" {
		status.Message = "正式版本检查失败，请稍后刷新；已有任务继续执行。"
	}
	return status, nil
}
func (s *Service) Start(ctx context.Context, admin, id, key, version string) (Task, error) {
	if admin == "" || !IDPattern.MatchString(key) {
		return Task{}, fmt.Errorf("%w: 更新任务标识无效", faults.ErrValidation)
	}
	if task, err := s.repo.ReplayNodeUpdate(ctx, admin, id, key, version); err != nil || task != nil {
		if task != nil {
			return *task, err
		}
		return Task{}, err
	}
	// Replays are checked transactionally by the repository, including older tasks.
	n, err := s.node(ctx, id)
	if err != nil {
		return Task{}, err
	}
	sha, err := s.releases.ResolveNodeArchive(ctx, version, n.AgentVersion)
	if err != nil {
		return Task{}, err
	}
	now := s.now().UTC()
	task := Task{ID: key, NodeID: id, TargetVersion: version, ArchiveSHA256: sha, State: "queued", Phase: "queued", Message: "等待节点领取", CreatedAt: now, UpdatedAt: now}
	event, err := audit.NewEvent(now, "administrator", admin, "node.update.requested", "node", id, "queued", map[string]any{"task_id": key, "target_version": version})
	if err != nil {
		return Task{}, err
	}
	return s.repo.CreateNodeUpdate(ctx, Record{Task: task, AdministratorID: admin}, event)
}
func (s *Service) Claim(ctx context.Context, id string) (*agentv1.NodeUpdateTask, error) {
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return nil, err
	}
	record, err := s.repo.ClaimNodeUpdate(ctx, id, hex.EncodeToString(token[:]), s.now().UTC())
	if err != nil || record == nil {
		return nil, err
	}
	return &agentv1.NodeUpdateTask{ID: record.Task.ID, Version: record.Task.TargetVersion, ArchiveSHA256: record.Task.ArchiveSHA256, Lease: record.Lease}, nil
}
func (s *Service) Cancel(ctx context.Context, admin, node, task string) (Task, error) {
	if admin == "" || !IDPattern.MatchString(task) {
		return Task{}, faults.ErrValidation
	}
	return s.repo.CancelNodeUpdate(ctx, admin, node, task, s.now().UTC())
}
func (s *Service) Report(ctx context.Context, id string, result agentv1.NodeUpdateResult) error {
	if !IDPattern.MatchString(result.ID) || !SHA256Pattern.MatchString(result.Lease) {
		return faults.ErrValidation
	}
	switch result.State {
	case "running", "verifying", "succeeded", "failed", "rolled_back", "uncertain":
	default:
		return faults.ErrValidation
	}
	// Messages are server-owned, never forwarded privileged child output.
	return s.repo.ReportNodeUpdate(ctx, id, result, s.now().UTC())
}

func ValidState(s string) bool {
	switch s {
	case "queued", "running", "verifying", "succeeded", "failed", "rolled_back", "cancelled", "uncertain":
		return true
	}
	return false
}
