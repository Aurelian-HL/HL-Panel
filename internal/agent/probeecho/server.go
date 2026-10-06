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
}

func Listen(port int) (*Server, error) {
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("probe echo port must be between 1 and 65535")
	}
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, fmt.Errorf("listen probe echo: %w", err)
	}
	server := &Server{listener: listener}
	server.wg.Add(1)
	go server.acceptLoop()
	return server, nil
}

func (s *Server) acceptLoop() {
	defer s.wg.Done()
	for {
		connection, err := s.listener.Accept()
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Temporary() {
				continue
			}
			return
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer connection.Close()
			_ = connection.SetDeadline(time.Now().Add(30 * time.Second))
			_, _ = io.Copy(connection, connection)
		}()
	}
}

func (s *Server) Close() error {
	if s == nil {
		return nil
	}
	var err error
	s.once.Do(func() { err = s.listener.Close() })
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
