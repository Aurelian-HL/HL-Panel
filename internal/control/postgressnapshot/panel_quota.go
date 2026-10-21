package postgressnapshot

import (
	"context"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
	"github.com/hongle/hl-panel/internal/control/panelquota"
)

func (s *Store) SetPanelQuotaProvider(provider panelquota.Provider) {
	s.quotaMu.Lock()
	defer s.quotaMu.Unlock()
	s.panelQuota = provider
}

func (s *Store) quotaProvider() panelquota.Provider {
	s.quotaMu.RLock()
	defer s.quotaMu.RUnlock()
	return s.panelQuota
}

func (s *Store) validatePanelGrowth(before, after panelquota.Usage) error {
	if provider := s.quotaProvider(); provider != nil {
		return provider().ValidateGrowth(before, after)
	}
	return nil
}

func (s *Store) PanelResourceUsage(ctx context.Context) (panelquota.Usage, error) {
	return transact(ctx, s, false, func(state *memoryrepo.Store) (panelquota.Usage, error) {
		return state.PanelResourceUsage(ctx)
	})
}
