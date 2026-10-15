// Package probeecho provides the node-local echo target used by the
// control-plane VLESS Reality protocol probe. It never listens on a public
// interface and does not log challenge payloads.
package probeecho

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

const DefaultPort = 19090

type Server struct {
	listener net.Listener
	wg       sync.WaitGroup
	once     sync.Once
	done     chan struct{}
	stop     chan struct{}
}

func Listen(port int) (*Server, error) {
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("probe echo port must be between 1 and 65535")
	}
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, fmt.Errorf("listen probe echo: %w", err)
	}
	server := &Server{listener: listener, done: make(chan struct{}), stop: make(chan struct{})}
	server.wg.Add(1)
	go server.acceptLoop()
	return server, nil
}

func (s *Server) acceptLoop() {
	defer s.wg.Done()
	defer close(s.done)
	var temporaryDelay time.Duration
	for {
		connection, err := s.listener.Accept()
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Temporary() {
				if temporaryDelay == 0 {
					temporaryDelay = 5 * time.Millisecond
				} else if temporaryDelay < time.Second {
					temporaryDelay *= 2
					if temporaryDelay > time.Second {
						temporaryDelay = time.Second
					}
				}
				timer := time.NewTimer(temporaryDelay)
				select {
				case <-timer.C:
				case <-s.stop:
					if !timer.Stop() {
						<-timer.C
					}
					return
				}
				continue
			}
			return
		}
		temporaryDelay = 0
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer connection.Close()
			_ = connection.SetDeadline(time.Now().Add(30 * time.Second))
			_, _ = io.Copy(connection, connection)
		}()
	}
}

// Done is closed when the accept loop stops. It lets a supervisor distinguish
// a live echo listener from one that exited unexpectedly and retry the bind.
func (s *Server) Done() <-chan struct{} {
	if s == nil {
		return nil
	}
	return s.done
}

func (s *Server) Close() error {
	if s == nil {
		return nil
	}
	var err error
	s.once.Do(func() {
		close(s.stop)
		err = s.listener.Close()
	})
	s.wg.Wait()
	return err
}

func (s *Server) Run(ctx context.Context) error {
	if s == nil {
		return nil
	}
	<-ctx.Done()
	return s.Close()
}
