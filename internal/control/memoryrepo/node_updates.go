package memoryrepo

import (
	"context"
	"fmt"
	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/nodeupdates"
	"github.com/hongle/hl-panel/internal/control/releases"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
	"regexp"
	"time"
)

func cloneUpdate(t nodeupdates.Task) nodeupdates.Task {
	if t.ClaimedAt != nil {
		v := *t.ClaimedAt
		t.ClaimedAt = &v
	}
	return t
}
func (s *Store) ReplayNodeUpdate(_ context.Context, admin, node, key, version string) (*nodeupdates.Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.replayUpdateLocked(admin, node, key, version)
}
func (s *Store) replayUpdateLocked(admin, node, key, version string) (*nodeupdates.Task, error) {
	r, ok := s.nodeUpdates[key]
	if !ok {
		return nil, nil
	}
	if r.AdministratorID != admin || r.Task.NodeID != node || r.Task.TargetVersion != version {
		return nil, faults.ErrIdempotencyConflict
	}
	task := cloneUpdate(r.Task)
	return &task, nil
}
func (s *Store) LatestNodeUpdate(_ context.Context, id string) (*nodeupdates.Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var latest *nodeupdates.Task
	for _, r := range s.nodeUpdates {
		if r.Task.NodeID == id && (latest == nil || r.Task.CreatedAt.After(latest.CreatedAt)) {
			v := cloneUpdate(r.Task)
			latest = &v
		}
	}
	return latest, nil
}
func (s *Store) CreateNodeUpdate(_ context.Context, r nodeupdates.Record, event audit.Event) (nodeupdates.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validUpdateRecord(r) || r.Task.State != "queued" || r.Task.Phase != "queued" {
		return nodeupdates.Task{}, faults.ErrValidation
	}
	if old, err := s.replayUpdateLocked(r.AdministratorID, r.Task.NodeID, r.Task.ID, r.Task.TargetVersion); err != nil || old != nil {
		if old != nil {
			return *old, err
		}
		return nodeupdates.Task{}, err
	}
	n, ok := s.nodes[r.Task.NodeID]
	if !ok || n.DeletedAt != nil {
		return nodeupdates.Task{}, faults.ErrNotFound
	}
	if !nodeupdates.Eligible(n, r.Task.CreatedAt) {
		return nodeupdates.Task{}, fmt.Errorf("%w: 节点离线或尚未安装更新服务，请先完成一次命令接入", faults.ErrConflict)
	}
	// First release deliberately serializes ALL nodes: no group overlap races.
	for _, old := range s.nodeUpdates {
		if old.Task.Active() {
			return nodeupdates.Task{}, fmt.Errorf("%w: 已有节点更新或结果待核实，请先完成该任务", faults.ErrConflict)
		}
	}
	if len(s.nodeUpdates) >= 2000 {
		return nodeupdates.Task{}, faults.ErrConflict
	}
	s.nodeUpdates[r.Task.ID] = r
	s.appendAuditLocked(event)
	return cloneUpdate(r.Task), nil
}
func (s *Store) ClaimNodeUpdate(_ context.Context, node, lease string, now time.Time) (*nodeupdates.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.nodes[node]
	if !ok || n.DeletedAt != nil {
		return nil, faults.ErrNotFound
	}
	for key, r := range s.nodeUpdates {
		if r.Task.NodeID != node || !r.Task.Active() {
			continue
		}
		if r.Lease == "" {
			if now.Sub(r.Task.CreatedAt) > 15*time.Minute {
				r.Task.State = "cancelled"
				r.Task.Phase = "expired"
				r.Task.Message = "节点未及时领取，任务已过期"
				r.Task.UpdatedAt = now
				if err := s.updateEventLocked(r, "expired", now); err != nil {
					return nil, err
				}
				s.nodeUpdates[key] = r
				return nil, nil
			}
			r.Lease = lease
			r.Task.ClaimedAt = &now
			r.Task.State = "running"
			r.Task.Phase = "claimed"
			r.Task.Message = "节点已领取"
			r.Task.UpdatedAt = now
			if err := s.updateEventLocked(r, "claimed", now); err != nil {
				return nil, err
			}
			s.nodeUpdates[key] = r
		}
		// A claimed task is never automatically reassigned, even after expiry.
		copy := r
		copy.Task = cloneUpdate(r.Task)
		return &copy, nil
	}
	return nil, nil
}

var backupPath = regexp.MustCompile(`^/var/backups/hl-panel-node/update-v[0-9]+\.[0-9]+\.[0-9]+-[A-Za-z0-9-]+$`)

func validUpdateRecord(r nodeupdates.Record) bool {
	t := r.Task
	if !nodeupdates.IDPattern.MatchString(t.ID) || r.AdministratorID == "" || !releases.ValidVersion(t.TargetVersion) || !nodeupdates.SHA256Pattern.MatchString(t.ArchiveSHA256) || !nodeupdates.ValidState(t.State) || t.CreatedAt.IsZero() || t.UpdatedAt.Before(t.CreatedAt) || (t.BackupDirectory != "" && !backupPath.MatchString(t.BackupDirectory)) {
		return false
	}
	if r.Lease == "" {
		return t.ClaimedAt == nil && t.BackupDirectory == "" && (t.State == "queued" || t.State == "cancelled")
	}
	return nodeupdates.SHA256Pattern.MatchString(r.Lease) && t.ClaimedAt != nil && !t.ClaimedAt.Before(t.CreatedAt) && !t.UpdatedAt.Before(*t.ClaimedAt) && t.State != "queued" && t.State != "cancelled"
}

func (s *Store) CancelNodeUpdate(_ context.Context, admin, node, id string, now time.Time) (nodeupdates.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.nodeUpdates[id]
	if !ok || r.Task.NodeID != node {
		return nodeupdates.Task{}, faults.ErrNotFound
	}
	if r.Task.State == "cancelled" {
		return cloneUpdate(r.Task), nil
	}
	if r.Lease != "" || r.Task.State != "queued" {
		return nodeupdates.Task{}, fmt.Errorf("%w: 节点已领取，不能取消正在执行的更新", faults.ErrConflict)
	}
	event, err := audit.NewEvent(now, "administrator", admin, "node.update.cancelled", "node", node, "cancelled", map[string]any{"task_id": id})
	if err != nil {
		return nodeupdates.Task{}, err
	}
	r.Task.State, r.Task.Phase, r.Task.Message, r.Task.UpdatedAt = "cancelled", "cancelled", "管理员已取消尚未领取的任务", now
	s.appendAuditLocked(event)
	s.nodeUpdates[id] = r
	return cloneUpdate(r.Task), nil
}

func (s *Store) ReportNodeUpdate(_ context.Context, node string, in agentv1.NodeUpdateResult, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.nodeUpdates[in.ID]
	if !ok || r.Task.NodeID != node || r.Lease == "" || r.Lease != in.Lease {
		return faults.ErrConflict
	}
	if !r.Task.Active() {
		if r.Task.State == in.State {
			return nil
		}
		return faults.ErrConflict
	}
	if in.BackupDirectory != "" && !backupPath.MatchString(in.BackupDirectory) {
		return faults.ErrValidation
	}
	state := in.State
	phase := in.Phase
	allowedPhases := map[string]bool{"download": true, "claimed": true, "verify": true, "verified": true, "rollback": true, "update_failed": true, "interrupted": true}
	if !allowedPhases[phase] {
		return faults.ErrValidation
	}
	if r.Task.BackupDirectory != "" && r.Task.BackupDirectory != in.BackupDirectory {
		return faults.ErrConflict
	}
	if state == "failed" && (in.BackupDirectory != "" || r.Task.State != "running") {
		return faults.ErrConflict
	}
	if state == "rolled_back" && in.BackupDirectory == "" {
		return faults.ErrValidation
	}
	if r.Task.State == "uncertain" && (state == "running" || state == "failed") {
		return faults.ErrConflict
	}
	if r.Task.State == "verifying" && state == "running" && phase != "rollback" {
		return faults.ErrConflict
	}
	if state == "succeeded" {
		n := s.nodes[node]
		if n.AgentVersion != r.Task.TargetVersion || r.Task.ClaimedAt == nil || n.LastHeartbeatAt == nil || !n.LastHeartbeatAt.After(*r.Task.ClaimedAt) || now.Sub(*n.LastHeartbeatAt) > 90*time.Second || in.VerifiedGeneration != n.DesiredGeneration || n.AppliedGeneration != n.DesiredGeneration {
			state = "verifying"
		}
		if n.DesiredGeneration > 0 {
			cfg := s.nodeConfigsByNode[node][n.DesiredGeneration]
			if cfg.ConfigSHA256 == "" || cfg.ConfigSHA256 != in.VerifiedSHA256 {
				state = "verifying"
			}
		}
	}
	messages := map[string]string{"running": "节点正在执行更新", "verifying": "程序已切换，等待新版本、配置和监听验收", "succeeded": "更新成功，目标版本、配置和监听已核实", "failed": "更新失败，旧程序未切换；请查看节点更新服务记录", "rolled_back": "更新失败，已恢复旧程序", "uncertain": "更新过程曾中断，结果待核实；未自动重试或放行后续节点"}
	message, ok := messages[state]
	if !ok {
		return faults.ErrValidation
	}
	if r.Task.State == state && r.Task.Phase == phase && r.Task.BackupDirectory == in.BackupDirectory {
		return nil
	}
	r.Task.State = state
	r.Task.Phase = phase
	r.Task.Message = message
	r.Task.BackupDirectory = in.BackupDirectory
	r.Task.UpdatedAt = now
	if err := s.updateEventLocked(r, state, now); err != nil {
		return err
	}
	s.nodeUpdates[in.ID] = r
	return nil
}
func (s *Store) updateEventLocked(r nodeupdates.Record, state string, now time.Time) error {
	event, err := audit.NewEvent(now, "node", r.Task.NodeID, "node.update."+state, "node", r.Task.NodeID, state, map[string]any{"task_id": r.Task.ID, "target_version": r.Task.TargetVersion})
	if err != nil {
		return err
	}
	s.appendAuditLocked(event)
	return nil
}
