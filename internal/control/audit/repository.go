package audit

import "context"

type Repository interface {
	AppendAudit(context.Context, Event) error
}

type Service struct {
	repository Repository
}

func NewService(repository Repository) *Service {
	return &Service{repository: repository}
}

func (s *Service) Record(ctx context.Context, event Event) error {
	return s.repository.AppendAudit(ctx, event)
}
