// Package runtime coordinates membership refresh with the long-running TCP
// front door. Snapshot errors never erase the last known good membership.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/hongle/hl-panel/internal/gateway/membership"
	"github.com/hongle/hl-panel/internal/routing/endpointrouter"
)

var (
	ErrSourceRequired  = errors.New("membership source is required")
	ErrRouterRequired  = errors.New("membership router is required")
	ErrServerRequired  = errors.New("gateway server is required")
	ErrRefreshInterval = errors.New("membership refresh interval must be positive")
)

type Source interface {
	Load(context.Context) (membership.Snapshot, error)
}

type Router interface {
	ReplaceVersionedMembership(uint64, []endpointrouter.Endpoint) error
}

type Server interface {
	Serve(context.Context) error
}

type ReachabilityMonitor interface {
	Run(context.Context)
}

type Config struct {
	Source              Source
	Router              Router
	Server              Server
	ReachabilityMonitor ReachabilityMonitor
	RefreshInterval     time.Duration
	Logger              *slog.Logger
}

type Runtime struct {
	source              Source
	router              Router
	server              Server
	reachabilityMonitor ReachabilityMonitor
	refreshInterval     time.Duration
	logger              *slog.Logger
}

type appliedSnapshot struct {
	revision uint64
	sha256   string
}

func New(config Config) (*Runtime, error) {
	if config.Source == nil {
		return nil, ErrSourceRequired
	}
	if config.Router == nil {
		return nil, ErrRouterRequired
	}
	if config.Server == nil {
		return nil, ErrServerRequired
	}
	if config.RefreshInterval <= 0 {
		return nil, ErrRefreshInterval
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	return &Runtime{
		source:              config.Source,
		router:              config.Router,
		server:              config.Server,
		reachabilityMonitor: config.ReachabilityMonitor,
		refreshInterval:     config.RefreshInterval,
		logger:              config.Logger,
	}, nil
}

func (r *Runtime) Run(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	initial, err := r.source.Load(ctx)
	if err != nil {
		return fmt.Errorf("load initial membership: %w", err)
	}
	state, err := r.apply(initial, appliedSnapshot{})
	if err != nil {
		return fmt.Errorf("apply initial membership: %w", err)
	}
	r.logger.Info("gateway membership activated", "revision", state.revision, "endpoints", len(initial.Endpoints))

	runContext, cancel := context.WithCancel(ctx)
	var background sync.WaitGroup
	background.Add(1)
	go func() {
		defer background.Done()
		r.watch(runContext, state)
	}()
	if r.reachabilityMonitor != nil {
		background.Add(1)
		go func() {
			defer background.Done()
			r.reachabilityMonitor.Run(runContext)
		}()
	}
	serveErr := r.server.Serve(runContext)
	cancel()
	background.Wait()
	if ctx.Err() != nil && serveErr == nil {
		return nil
	}
	return serveErr
}

func (r *Runtime) watch(ctx context.Context, state appliedSnapshot) {
	ticker := time.NewTicker(r.refreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			snapshot, err := r.source.Load(ctx)
			if err != nil {
				if ctx.Err() == nil {
					r.logger.Warn("membership refresh failed; retaining last known good snapshot", "error", normalizedError(err))
				}
				continue
			}
			next, err := r.apply(snapshot, state)
			if err != nil {
				r.logger.Warn("membership snapshot rejected; retaining last known good snapshot", "revision", snapshot.Revision, "error", normalizedError(err))
				continue
			}
			if next != state {
				state = next
				r.logger.Info("gateway membership activated", "revision", state.revision, "endpoints", len(snapshot.Endpoints))
			}
		}
	}
}

func (r *Runtime) apply(snapshot membership.Snapshot, current appliedSnapshot) (appliedSnapshot, error) {
	if snapshot.Revision == 0 {
		return current, errors.New("membership revision must be greater than zero")
	}
	if snapshot.Endpoints == nil {
		return current, errors.New("membership endpoints must be an array")
	}
	if current.revision != 0 {
		if snapshot.Revision < current.revision {
			return current, fmt.Errorf("membership revision %d is older than active revision %d", snapshot.Revision, current.revision)
		}
		if snapshot.Revision == current.revision {
			if snapshot.SHA256 != current.sha256 {
				return current, fmt.Errorf("membership revision %d changed content without advancing", snapshot.Revision)
			}
			return current, nil
		}
	}
	if err := r.router.ReplaceVersionedMembership(snapshot.Revision, snapshot.Endpoints); err != nil {
		return current, err
	}
	return appliedSnapshot{revision: snapshot.Revision, sha256: snapshot.SHA256}, nil
}

func normalizedError(err error) string {
	message := strings.Join(strings.Fields(err.Error()), " ")
	if len(message) > 512 {
		return message[:512]
	}
	return message
}
