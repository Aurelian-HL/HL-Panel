// Package tcpproxy provides a small protocol-agnostic single-endpoint front
// door. It forwards each accepted TCP connection to exactly one backend chosen
// by a server-side selector. VLESS/Reality and SOCKS5 bytes are deliberately
// not parsed here; the selected backend remains responsible for its protocol.
package tcpproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	ErrSelectorRequired = errors.New("backend selector is required")
	ErrListenerRequired = errors.New("listener is required")
	ErrAlreadyServing   = errors.New("gateway server can only be served once")
)

type Backend struct {
	ID                 string
	Address            string
	MembershipRevision uint64
}

// Selector reserves one backend for a new connection and releases the
// reservation after both sides close. It must exclude unhealthy or draining
// members before returning a backend.
type Selector interface {
	Select() (Backend, error)
	Release(string) error
}

// KeyedSelector is optional. Rendezvous-capable selectors use a stable client
// key while simple selectors continue to satisfy Selector alone.
type KeyedSelector interface {
	SelectKey(string) (Backend, error)
}

// ReachabilitySelector is optional. Gateways use it to immediately veto a
// backend whose TCP dial failed. The health monitor can later clear the veto
// after a successful probe.
type ReachabilitySelector interface {
	SetLocalReachability(endpointID, observedAddress string, reachable bool) error
}

// VersionedReachabilitySelector is an optional stronger form of
// ReachabilitySelector. The revision ties a dial result to the membership
// snapshot from which the backend was selected, preventing stale connection
// results from changing the state of a newer membership.
type VersionedReachabilitySelector interface {
	SetLocalReachabilityAtRevision(endpointID, observedAddress string, revision uint64, reachable bool) error
}

type DialContext func(context.Context, string, string) (net.Conn, error)

type Server struct {
	listener        net.Listener
	selector        Selector
	dial            DialContext
	logger          *slog.Logger
	dialTimeout     time.Duration
	maxDialAttempts int
	mu              sync.Mutex
	connections     map[net.Conn]struct{}
	handlers        sync.WaitGroup
	started         atomic.Bool
	closeOnce       sync.Once
	closed          chan struct{}
}

type Config struct {
	Listener    net.Listener
	Selector    Selector
	Dial        DialContext
	Logger      *slog.Logger
	DialTimeout time.Duration
	// MaxDialAttempts bounds how many different backends a client connection
	// may try before it is closed. Zero uses the default of three attempts.
	MaxDialAttempts int
}

func New(config Config) (*Server, error) {
	if config.Listener == nil {
		return nil, ErrListenerRequired
	}
	if config.Selector == nil {
		return nil, ErrSelectorRequired
	}
	if config.Dial == nil {
		config.Dial = (&net.Dialer{}).DialContext
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	if config.DialTimeout <= 0 {
		config.DialTimeout = 10 * time.Second
	}
	if config.MaxDialAttempts <= 0 {
		config.MaxDialAttempts = 3
	}
	return &Server{
		listener:        config.Listener,
		selector:        config.Selector,
		dial:            config.Dial,
		logger:          config.Logger,
		dialTimeout:     config.DialTimeout,
		maxDialAttempts: config.MaxDialAttempts,
		connections:     make(map[net.Conn]struct{}),
		closed:          make(chan struct{}),
	}, nil
}

func (s *Server) Addr() net.Addr { return s.listener.Addr() }

// Serve accepts until the context is canceled or the listener fails. A
// canceled context is returned as nil after all tracked connections are closed.
func (s *Server) Serve(ctx context.Context) error {
	if !s.started.CompareAndSwap(false, true) {
		return ErrAlreadyServing
	}
	if ctx == nil {
		ctx = context.Background()
	}
	defer func() {
		_ = s.Close()
		s.handlers.Wait()
	}()
	go func() {
		select {
		case <-ctx.Done():
			_ = s.Close()
		case <-s.closed:
		}
	}()
	acceptRetryDelay := 5 * time.Millisecond
	for {
		client, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.closed:
				return nil
			default:
			}
			if temporary(err) {
				if !waitForAcceptRetry(s.closed, acceptRetryDelay) {
					return nil
				}
				if acceptRetryDelay < time.Second {
					acceptRetryDelay *= 2
					if acceptRetryDelay > time.Second {
						acceptRetryDelay = time.Second
					}
				}
				continue
			}
			return fmt.Errorf("accept client connection: %w", err)
		}
		acceptRetryDelay = 5 * time.Millisecond
		if !s.track(client) {
			return nil
		}
		s.handlers.Add(1)
		go func() {
			defer s.handlers.Done()
			s.handle(ctx, client)
		}()
	}
}

// waitForAcceptRetry keeps transient listener failures from creating a busy
// loop while allowing Close to interrupt the wait immediately. A fixed sleep
// here used to leave Serve blocked for the whole retry interval during a
// restart.
func waitForAcceptRetry(closed <-chan struct{}, delay time.Duration) bool {
	if delay <= 0 {
		return true
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-closed:
		return false
	case <-timer.C:
		return true
	}
}

func (s *Server) Close() error {
	var err error
	s.closeOnce.Do(func() {
		close(s.closed)
		err = s.listener.Close()
		s.mu.Lock()
		connections := make([]net.Conn, 0, len(s.connections))
		for connection := range s.connections {
			connections = append(connections, connection)
			delete(s.connections, connection)
		}
		s.mu.Unlock()
		for _, connection := range connections {
			_ = connection.Close()
		}
	})
	return err
}

func (s *Server) handle(parent context.Context, client net.Conn) {
	defer s.untrack(client)
	attempted := make(map[string]struct{}, s.maxDialAttempts)
	// Bound the complete failover sequence, rather than granting every
	// backend attempt a fresh timeout. A dead endpoint must not make one client
	// wait maxDialAttempts times the configured timeout before failover ends.
	dialDeadline := time.Now().Add(s.dialTimeout)
	var backend Backend
	var backendConnection net.Conn
	var err error
	for attempt := 0; attempt < s.maxDialAttempts; attempt++ {
		backend, err = s.selectBackend(client.RemoteAddr())
		if err != nil {
			break
		}
		if _, exists := attempted[backend.ID]; exists {
			_ = s.releaseBackend(backend)
			err = errors.New("selector returned an already attempted backend")
			break
		}
		attempted[backend.ID] = struct{}{}
		if validationErr := validateBackend(backend); validationErr != nil {
			_ = s.releaseBackend(backend)
			err = validationErr
			continue
		}
		remaining := time.Until(dialDeadline)
		if remaining <= 0 {
			err = context.DeadlineExceeded
			_ = s.releaseBackend(backend)
			break
		}
		attemptsLeft := time.Duration(s.maxDialAttempts - attempt)
		attemptTimeout := remaining / attemptsLeft
		if attemptTimeout <= 0 {
			attemptTimeout = remaining
		}
		dialContext, cancel := context.WithTimeout(parent, attemptTimeout)
		backendConnection, err = s.dial(dialContext, "tcp", backend.Address)
		cancel()
		if err == nil {
			if backendConnection == nil {
				err = errors.New("dialer returned no connection")
				_ = s.releaseBackend(backend)
				continue
			}
			break
		}
		// A DialContext implementation may return a partially established
		// connection together with an error. It cannot be forwarded, but it
		// still owns descriptors and must be closed before failover continues.
		if backendConnection != nil {
			_ = backendConnection.Close()
			backendConnection = nil
		}
		// A gateway shutdown/restart cancels the parent context. That is not
		// evidence that the selected backend is unreachable, so do not persist
		// a local veto that would survive the restart until the next probe.
		if parent.Err() == nil {
			if markErr := s.setLocalReachability(backend, false); markErr != nil {
				s.logger.Debug("backend local reachability update failed", "backend_id", backend.ID, "error", sanitizeError(markErr))
			}
		}
		if releaseErr := s.releaseBackend(backend); releaseErr != nil {
			s.logger.Warn("backend reservation release failed", "backend_id", backend.ID, "error", sanitizeError(releaseErr))
		}
		if parent.Err() != nil {
			break
		}
	}
	if err != nil || backendConnection == nil {
		_ = client.Close()
		return
	}
	// A dialer may return a connection at the same time the gateway is being
	// stopped. That connection is immediately closed by the shutdown path and
	// must not be recorded as a successful reachability recovery.
	if parent.Err() == nil {
		if clearErr := s.setLocalReachability(backend, true); clearErr != nil {
			s.logger.Debug("backend local reachability recovery update failed", "backend_id", backend.ID, "error", sanitizeError(clearErr))
		}
	}
	defer func() {
		if releaseErr := s.releaseBackend(backend); releaseErr != nil {
			s.logger.Warn("backend reservation release failed", "backend_id", backend.ID, "error", sanitizeError(releaseErr))
		}
	}()
	if !s.track(backendConnection) {
		_ = client.Close()
		return
	}
	defer s.untrack(backendConnection)
	defer backendConnection.Close()
	defer client.Close()
	proxyBidirectionally(client, backendConnection)
}

func (s *Server) releaseBackend(backend Backend) error {
	if strings.TrimSpace(backend.ID) == "" {
		return nil
	}
	return s.selector.Release(backend.ID)
}

func (s *Server) setLocalReachability(backend Backend, reachable bool) error {
	if versioned, ok := s.selector.(VersionedReachabilitySelector); ok && backend.MembershipRevision != 0 {
		return versioned.SetLocalReachabilityAtRevision(backend.ID, backend.Address, backend.MembershipRevision, reachable)
	}
	if legacy, ok := s.selector.(ReachabilitySelector); ok {
		return legacy.SetLocalReachability(backend.ID, backend.Address, reachable)
	}
	return nil
}

func proxyBidirectionally(client, backend net.Conn) {
	results := make(chan error, 2)
	go copyHalf(results, backend, client)
	go copyHalf(results, client, backend)
	firstErr := <-results
	// EOF in one direction is a half-close, not a request to tear down the
	// opposite direction. Preserve the backend response after a client
	// CloseWrite. A real copy error still aborts both sides to unblock the
	// remaining goroutine.
	if firstErr != nil {
		_ = client.Close()
		_ = backend.Close()
	}
	secondErr := <-results
	if secondErr != nil {
		_ = client.Close()
		_ = backend.Close()
	}
}

func copyHalf(results chan<- error, destination io.Writer, source io.Reader) {
	_, err := io.Copy(destination, source)
	if err == nil {
		if closeWriter, ok := destination.(interface{ CloseWrite() error }); ok {
			_ = closeWriter.CloseWrite()
		}
	}
	results <- err
}

func (s *Server) track(connection net.Conn) bool {
	s.mu.Lock()
	select {
	case <-s.closed:
		s.mu.Unlock()
		_ = connection.Close()
		return false
	default:
	}
	s.connections[connection] = struct{}{}
	s.mu.Unlock()
	return true
}

func (s *Server) untrack(connection net.Conn) {
	s.mu.Lock()
	delete(s.connections, connection)
	s.mu.Unlock()
}

func validateBackend(backend Backend) error {
	if strings.TrimSpace(backend.ID) == "" || strings.TrimSpace(backend.Address) == "" {
		return errors.New("backend id and address are required")
	}
	if strings.TrimSpace(backend.Address) != backend.Address || strings.ContainsAny(backend.Address, "\r\n\t @?#") {
		return errors.New("backend address contains whitespace, URL, or credential characters")
	}
	host, portText, err := net.SplitHostPort(backend.Address)
	if err != nil || strings.TrimSpace(host) == "" {
		return errors.New("backend address must be a host:port pair")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("backend port must be between 1 and 65535")
	}
	return nil
}

func (s *Server) selectBackend(remoteAddress net.Addr) (Backend, error) {
	keyed, ok := s.selector.(KeyedSelector)
	if !ok {
		return s.selector.Select()
	}
	return keyed.SelectKey(clientAffinityKey(remoteAddress))
}

func clientAffinityKey(address net.Addr) string {
	if address == nil {
		return ""
	}
	value := address.String()
	host, _, err := net.SplitHostPort(value)
	if err == nil && host != "" {
		return host
	}
	return value
}

func temporary(err error) bool {
	if networkError, ok := err.(net.Error); ok {
		return networkError.Temporary()
	}
	return false
}

func sanitizeError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.Join(strings.Fields(err.Error()), " ")
	if len(message) > 512 {
		return message[:512]
	}
	return message
}
