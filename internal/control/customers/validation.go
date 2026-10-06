package customers

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/hongle/hl-panel/internal/control/faults"
)

func validID(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func validateMutation(idempotencyKey string, revision int64, creating bool) error {
	if len(idempotencyKey) < 8 || len(idempotencyKey) > 128 {
		return fmt.Errorf("%w: Idempotency-Key must contain 8 to 128 visible ASCII characters", faults.ErrValidation)
	}
	for _, r := range idempotencyKey {
		if r < 33 || r > 126 {
			return fmt.Errorf("%w: Idempotency-Key must contain visible ASCII characters", faults.ErrValidation)
		}
	}
	if creating && revision != 0 || !creating && (revision < 1 || revision == math.MaxInt64) {
		return fmt.Errorf("%w: revision must be zero for create and a positive current revision for update", faults.ErrValidation)
	}
	return nil
}

func normalizeCustomerInput(input CustomerInput, creating bool) (CustomerInput, error) {
	if err := validateMutation(input.IdempotencyKey, input.Revision, creating); err != nil {
		return input, err
	}
	return normalizeCustomerFields(input, creating)
}

func normalizeCustomerFields(input CustomerInput, creating bool) (CustomerInput, error) {
	input.Username = strings.ToLower(strings.TrimSpace(input.Username))
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.UserGroupID = strings.TrimSpace(input.UserGroupID)
	if len(input.Username) < 1 || len(input.Username) > 64 {
		return input, fmt.Errorf("%w: username must contain 1 to 64 characters", faults.ErrValidation)
	}
	for _, r := range input.Username {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' || r == '@') {
			return input, fmt.Errorf("%w: username allows letters, digits, dot, underscore, at sign and hyphen", faults.ErrValidation)
		}
	}
	if !utf8.ValidString(input.DisplayName) || utf8.RuneCountInString(input.DisplayName) > 128 {
		return input, fmt.Errorf("%w: display_name must contain at most 128 characters", faults.ErrValidation)
	}
	if input.UserGroupID != "" && !validID(input.UserGroupID) {
		return input, fmt.Errorf("%w: user_group_id must be a valid ID when provided", faults.ErrValidation)
	}
	if creating && input.Password == "" || len(input.Password) > 1024 || !utf8.ValidString(input.Password) {
		return input, fmt.Errorf("%w: password is required on create and must be at most 1024 bytes", faults.ErrValidation)
	}
	if input.TrafficLimitBytes < 0 || input.MaxRules < 0 || input.MaxRules > 1_000_000 || input.SpeedLimitMbps < 0 || input.SpeedLimitMbps > 10_000_000 || input.IPLimit < 0 || input.IPLimit > 10_000_000 || input.ConnectionLimit < 0 || input.ConnectionLimit > 100_000_000 {
		return input, fmt.Errorf("%w: limits must be nonnegative and within supported ranges; zero means unlimited", faults.ErrValidation)
	}
	if input.ExpiresAt != nil {
		expires := input.ExpiresAt.UTC()
		if expires.Year() < 1970 || expires.Year() > 9999 {
			return input, fmt.Errorf("%w: expires_at must be between years 1970 and 9999", faults.ErrValidation)
		}
		input.ExpiresAt = &expires
	}
	return input, nil
}

func normalizeUserGroupInput(input UserGroupInput, creating bool) (UserGroupInput, error) {
	if err := validateMutation(input.IdempotencyKey, input.Revision, creating); err != nil {
		return input, err
	}
	return normalizeUserGroupFields(input)
}

func normalizeUserGroupFields(input UserGroupInput) (UserGroupInput, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	if !utf8.ValidString(input.Name) || input.Name == "" || utf8.RuneCountInString(input.Name) > 128 {
		return input, fmt.Errorf("%w: name must contain 1 to 128 characters", faults.ErrValidation)
	}
	if !utf8.ValidString(input.Description) || utf8.RuneCountInString(input.Description) > 1024 {
		return input, fmt.Errorf("%w: description must contain at most 1024 characters", faults.ErrValidation)
	}
	var err error
	input.AllowedEntryGroupIDs, err = normalizeGroupIDs(input.AllowedEntryGroupIDs)
	if err != nil {
		return input, err
	}
	input.AllowedExitGroupIDs, err = normalizeGroupIDs(input.AllowedExitGroupIDs)
	return input, err
}

func normalizeGroupIDs(input []string) ([]string, error) {
	if len(input) > 1000 {
		return nil, fmt.Errorf("%w: a user group can authorize at most 1000 device groups per role", faults.ErrValidation)
	}
	values := make([]string, 0, len(input))
	seen := make(map[string]bool, len(input))
	for _, value := range input {
		value = strings.TrimSpace(value)
		if !validID(value) {
			return nil, fmt.Errorf("%w: authorized device group ID is invalid", faults.ErrValidation)
		}
		if seen[value] {
			return nil, fmt.Errorf("%w: authorized device group ID is duplicated", faults.ErrValidation)
		}
		seen[value] = true
		values = append(values, value)
	}
	sort.Strings(values)
	return values, nil
}
