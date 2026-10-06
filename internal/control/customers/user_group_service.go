package customers

import (
	"context"
	"fmt"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/idgen"
)

func (s *Service) ListUserGroups(ctx context.Context) ([]UserGroup, error) {
	items, err := s.repository.ListUserGroups(ctx)
	if items == nil && err == nil {
		items = []UserGroup{}
	}
	return items, err
}

func (s *Service) UserGroup(ctx context.Context, id string) (UserGroup, error) {
	if !validID(id) {
		return UserGroup{}, fmt.Errorf("%w: invalid user group ID", faults.ErrValidation)
	}
	return s.repository.UserGroup(ctx, id)
}

func (s *Service) CreateUserGroup(ctx context.Context, adminID string, input UserGroupInput) (UserGroup, bool, error) {
	input, err := normalizeUserGroupInput(input, true)
	if err != nil {
		return UserGroup{}, false, err
	}
	id, err := idgen.New("ugrp")
	if err != nil {
		return UserGroup{}, false, err
	}
	now := s.now().UTC()
	item := userGroupFromInput(input)
	item.ID, item.CreatedAt, item.UpdatedAt, item.Revision = id, now, now, 1
	return s.saveUserGroup(ctx, adminID, "", input, item, "user_group.create")
}

func (s *Service) UpdateUserGroup(ctx context.Context, adminID, id string, input UserGroupInput) (UserGroup, bool, error) {
	if !validID(id) {
		return UserGroup{}, false, fmt.Errorf("%w: invalid user group ID", faults.ErrValidation)
	}
	input, err := normalizeUserGroupInput(input, false)
	if err != nil {
		return UserGroup{}, false, err
	}
	previous, err := s.repository.UserGroup(ctx, id)
	if err != nil {
		return UserGroup{}, false, err
	}
	item := userGroupFromInput(input)
	item.ID, item.CreatedAt = id, previous.CreatedAt
	item.UpdatedAt, item.Revision = s.now().UTC(), input.Revision+1
	return s.saveUserGroup(ctx, adminID, id, input, item, "user_group.update")
}

func (s *Service) saveUserGroup(ctx context.Context, adminID, requestID string, input UserGroupInput, item UserGroup, action string) (UserGroup, bool, error) {
	requestHash, err := requestFingerprint(struct {
		ID    string         `json:"id"`
		Input UserGroupInput `json:"input"`
	}{requestID, input})
	if err != nil {
		return UserGroup{}, false, err
	}
	event, err := audit.NewEvent(item.UpdatedAt, "administrator", adminID, action, "user_group", item.ID, "succeeded", map[string]any{
		"name": item.Name, "allowed_entry_group_ids": item.AllowedEntryGroupIDs,
		"allowed_exit_group_ids": item.AllowedExitGroupIDs, "allow_direct": item.AllowDirect,
		"revision": item.Revision,
	})
	if err != nil {
		return UserGroup{}, false, err
	}
	return s.repository.SaveUserGroup(ctx, SaveUserGroupInput{
		UserGroup: item, ExpectedRevision: input.Revision, IdempotencyKey: input.IdempotencyKey,
		RequestSHA256: requestHash, CreatedBy: adminID,
	}, event)
}

func userGroupFromInput(input UserGroupInput) UserGroup {
	return UserGroup{
		Name: input.Name, Description: input.Description,
		AllowedEntryGroupIDs: input.AllowedEntryGroupIDs, AllowedExitGroupIDs: input.AllowedExitGroupIDs,
		AllowDirect: input.AllowDirect,
	}
}
