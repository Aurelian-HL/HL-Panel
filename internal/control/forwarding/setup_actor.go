package forwarding

import "context"

// VLESSSetupAdministrator resumes customer rules only for their durable author.
func (s *Service) VLESSSetupAdministrator(ctx context.Context, ruleID string) (string, error) {
	if source, ok := s.repository.(interface {
		VLESSSetupAdministrator(context.Context, string) (string, error)
	}); ok {
		return source.VLESSSetupAdministrator(ctx, ruleID)
	}
	return "", nil
}
