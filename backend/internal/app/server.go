package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"

	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/config"
	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/health"
)

const maxHeaderBytes = 1 << 20

// Server owns one HTTP listener and its shutdown goroutine. A Server is
// intentionally single-use so readiness and shutdown cannot race across runs.
type Server struct {
	name      string
	config    config.HTTPServer
	server    *http.Server
	readiness *health.State
	logger    *slog.Logger
	started   chan struct{}
	running   atomic.Bool
}

// NewServer constructs a bounded standard-library HTTP server.
func NewServer(
	name string,
	configured config.HTTPServer,
	handler http.Handler,
	readiness *health.State,
	logger *slog.Logger,
) *Server {
	if handler == nil {
		handler = http.NotFoundHandler()
	}
	if readiness == nil {
		readiness = health.NewState()
	}
	if logger == nil {
		logger = slog.Default()
	}

	return &Server{
		name:      name,
		config:    configured,
		readiness: readiness,
		logger:    logger,
		started:   make(chan struct{}),
		server: &http.Server{
			Handler:           handler,
			ReadHeaderTimeout: configured.ReadHeaderTimeout,
			ReadTimeout:       configured.ReadTimeout,
			WriteTimeout:      configured.WriteTimeout,
			IdleTimeout:       configured.IdleTimeout,
			MaxHeaderBytes:    maxHeaderBytes,
			ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
		},
	}
}

// Started closes after the listener is owned and readiness has been published.
// It lets tests and supervisors synchronize without sleep-based polling.
func (s *Server) Started() <-chan struct{} {
	return s.started
}

// Run binds the configured address and owns the listener until shutdown.
func (s *Server) Run(ctx context.Context) error {
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(ctx, "tcp", s.config.Address)
	if err != nil {
		return fmt.Errorf("%s listen: %w", s.name, err)
	}
	return s.Serve(ctx, listener)
}

// Serve owns listener and one goroutine running http.Server.Serve. Every return
// path joins that goroutine. Cancellation first withdraws readiness, then asks
// active requests to drain within ShutdownTimeout, and finally closes remaining
// connections so process termination stays bounded.
func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	if !s.running.CompareAndSwap(false, true) {
		if err := listener.Close(); err != nil {
			return fmt.Errorf("%s HTTP server has already been started; close rejected listener: %w", s.name, err)
		}
		return fmt.Errorf("%s HTTP server has already been started", s.name)
	}

	s.readiness.SetReady(true)
	close(s.started)
	s.logger.Info("HTTP server listening", "service", s.name, "address", listener.Addr().String())

	serveDone := make(chan error, 1)
	go func() {
		serveDone <- s.server.Serve(listener)
	}()

	select {
	case err := <-serveDone:
		s.readiness.SetReady(false)
		if err == nil || errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("%s HTTP server: %w", s.name, err)
	case <-ctx.Done():
		s.readiness.SetReady(false)
		s.logger.Info("HTTP server draining", "service", s.name)

		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.config.ShutdownTimeout)
		shutdownErr := s.server.Shutdown(shutdownCtx)
		cancel()

		if shutdownErr != nil {
			if closeErr := s.server.Close(); closeErr != nil && !errors.Is(closeErr, http.ErrServerClosed) {
				shutdownErr = errors.Join(shutdownErr, closeErr)
			}
		}

		serveErr := <-serveDone
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			shutdownErr = errors.Join(shutdownErr, serveErr)
		}
		if shutdownErr != nil {
			return fmt.Errorf("%s HTTP shutdown: %w", s.name, shutdownErr)
		}
		return nil
	}
}
