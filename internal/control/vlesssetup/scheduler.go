package vlesssetup

import (
	"context"
	"log/slog"

	"github.com/hongle/hl-panel/internal/control/forwarding"
)

const setupWorkers = 2
const setupQueueCapacity = 256

type setupJob struct {
	administratorID string
	rule            forwarding.Rule
	logger          *slog.Logger
}

// Notify coalesces durable recovery requests without polling the database faster.
func (s *Service) Notify() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Schedule returns after enqueueing committed work. At most two workers drain
// the bounded queue continuously; excess work stays durable for reconciliation.
// HTTP cancellation must not cancel a rule that has already been committed.
func (s *Service) Schedule(administratorID string, rule forwarding.Rule, logger *slog.Logger) {
	if rule.Paused || rule.EffectiveIngressProtocol() != forwarding.IngressVLESSReality ||
		rule.IngressReadiness() != forwarding.IngressReady || rule.EgressMode != forwarding.EgressDirect ||
		rule.PendingActivationReason() != forwarding.ActivationReasonVLESSRuntimeMaterialPending {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	key := administratorID + "\x00" + rule.ID
	s.queueMu.Lock()
	if previous, ok := s.queued[key]; ok {
		if rule.Revision >= previous.rule.Revision {
			s.queued[key] = setupJob{administratorID, rule, logger}
		}
		s.queueMu.Unlock()
		return
	}
	if len(s.queue) >= setupQueueCapacity {
		s.queueMu.Unlock()
		s.Notify()
		return
	}
	s.queued[key] = setupJob{administratorID, rule, logger}
	s.queue = append(s.queue, key)
	if s.workers < setupWorkers {
		s.workers++
		go s.drainQueue()
	}
	s.queueMu.Unlock()
}

func (s *Service) drainQueue() {
	for {
		s.queueMu.Lock()
		if len(s.queue) == 0 {
			s.workers--
			s.queueMu.Unlock()
			return
		}
		key := s.queue[0]
		s.queue[0] = ""
		s.queue = s.queue[1:]
		job := s.queued[key]
		delete(s.queued, key)
		s.queueMu.Unlock()

		ctx, cancel := context.WithTimeout(context.Background(), s.reconcileTimeout)
		rule := job.rule
		var err error
		// Reload queued work so edits, pauses and deletions supersede an old save.
		if s.rules != nil {
			rule, err = s.rules.GetForAdministrator(ctx, job.administratorID, rule.ID)
		}
		if err == nil && !rule.Paused {
			err = s.Ensure(ctx, job.administratorID, rule)
		}
		cancel()
		if err != nil {
			job.logger.Warn("vless forwarding rule remains pending activation", "rule_id", job.rule.ID, "error", err)
		}
	}
}
