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
	ID      string
	Address string
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

type DialContext func(context.Context, string, string) (net.Conn, error)

type Server struct {
	listener    net.Listener
	selector    Selector
	dial        DialContext
	logger      *slog.Logger
	dialTimeout time.Duration
	mu          sync.Mutex
	connections map[net.Conn]struct{}
	handlers    sync.WaitGroup
	started     atomic.Bool
	closeOnce   sync.Once
	closed      chan struct{}
}

type Config struct {
	Listener    net.Listener
	Selector    Selector
	Dial        DialContext
	Logger      *slog.Logger
	DialTimeout time.Duration
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
	return &Server{
		listener:    config.Listener,
		selector:    config.Selector,
		dial:        config.Dial,
		logger:      config.Logger,
		dialTimeout: config.DialTimeout,
		connections: make(map[net.Conn]struct{}),
		closed:      make(chan struct{}),
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
	for {
		client, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.closed:
				return nil
			default:
			}
			if temporary(err) {
				time.Sleep(25 * time.Millisecond)
				continue
			}
			return fmt.Errorf("accept client connection: %w", err)
		}
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
	backend, err := s.selectBackend(client.RemoteAddr())
	if err != nil {
		_ = client.Close()
		return
	}
	defer func() {
		if releaseErr := s.selector.Release(backend.ID); releaseErr != nil {
			s.logger.Warn("backend reservation release failed", "backend_id", backend.ID, "error", sanitizeError(releaseErr))
		}
	}()
	if err := validateBackend(backend); err != nil {
		_ = client.Close()
		return
	}
	dialContext, cancel := context.WithTimeout(parent, s.dialTimeout)
	backendConnection, err := s.dial(dialContext, "tcp", backend.Address)
	cancel()
	if err != nil {
		_ = client.Close()
		return
	}
	if !s.track(backendConnection) {
		_ = client.Close()
		return
	}
	defer s.untrack(backendConnection)
	defer backendConnection.Close()
	defer client.Close()
	proxyBidirectionally(client, backendConnection)
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
