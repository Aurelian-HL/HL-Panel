package customers

import (
	"fmt"
	"time"

	"github.com/hongle/hl-panel/internal/control/faults"
)

// ValidateStoredCustomer checks public business data without requiring a raw
// password or inspecting its private hash. Persistence adapters separately
// validate the hash from their confidential snapshot representation.
func ValidateStoredCustomer(item Customer) error {
	if !validID(item.ID) || item.Revision < 1 || item.TrafficUsedBytes < 0 {
		return fmt.Errorf("%w: invalid stored customer identity, revision or usage", faults.ErrValidation)
	}
	input, err := normalizeCustomerFields(CustomerInput{
		Username: item.Username, DisplayName: item.DisplayName, UserGroupID: item.UserGroupID,
		Disabled: item.Disabled, ExpiresAt: item.ExpiresAt, TrafficLimitBytes: item.TrafficLimitBytes,
		MaxRules: item.MaxRules, SpeedLimitMbps: item.SpeedLimitMbps, IPLimit: item.IPLimit, ConnectionLimit: item.ConnectionLimit,
	}, false)
	if err != nil {
		return err
	}
	if input.Username != item.Username || input.DisplayName != item.DisplayName || input.UserGroupID != item.UserGroupID {
		return fmt.Errorf("%w: stored customer fields are not canonical", faults.ErrValidation)
	}
	return validateStoredTimes(item.CreatedAt, item.UpdatedAt)
}

// ValidateStoredUserGroup checks user-authored authorization syntax. Existing
// references and their entry/exit roles remain transactional repository checks.
func ValidateStoredUserGroup(item UserGroup) error {
	if !validID(item.ID) || item.Revision < 1 {
		return fmt.Errorf("%w: invalid stored user group identity or revision", faults.ErrValidation)
	}
	input, err := normalizeUserGroupFields(UserGroupInput{
		Name: item.Name, Description: item.Description, AllowedEntryGroupIDs: item.AllowedEntryGroupIDs,
		AllowedExitGroupIDs: item.AllowedExitGroupIDs, AllowDirect: item.AllowDirect,
	})
	if err != nil {
		return err
	}
	if input.Name != item.Name || input.Description != item.Description {
		return fmt.Errorf("%w: stored user group fields are not canonical", faults.ErrValidation)
	}
	for _, ids := range [][]string{item.AllowedEntryGroupIDs, item.AllowedExitGroupIDs} {
		for _, id := range ids {
			if !validID(id) {
				return fmt.Errorf("%w: invalid stored authorization reference", faults.ErrValidation)
			}
		}
	}
	return validateStoredTimes(item.CreatedAt, item.UpdatedAt)
}

func validateStoredTimes(createdAt, updatedAt time.Time) error {
	if createdAt.UTC().Year() < 1970 || createdAt.UTC().Year() > 9999 || updatedAt.UTC().Year() < 1970 || updatedAt.UTC().Year() > 9999 || updatedAt.Before(createdAt) {
		return fmt.Errorf("%w: invalid stored lifecycle timestamps", faults.ErrValidation)
	}
	return nil
}
