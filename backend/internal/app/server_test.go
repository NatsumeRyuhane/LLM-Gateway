package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/config"
	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/health"
)

func TestServerStartsServesAndShutsDown(t *testing.T) {
	t.Parallel()

	readiness := health.NewState()
	mux := http.NewServeMux()
	readiness.Register(mux)
	configured := config.GatewayHTTPDefaults()
	configured.ShutdownTimeout = time.Second

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}

	server := NewServer("test", configured, mux, readiness, discardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- server.Serve(ctx, listener)
	}()

	waitForSignal(t, server.Started(), "server start")
	if !readiness.IsReady() {
		t.Fatal("readiness = false after server start")
	}

	client := &http.Client{Timeout: time.Second}
	response, err := client.Get("http://" + listener.Addr().String() + health.ReadinessPath)
	if err != nil {
		t.Fatalf("GET readiness error = %v", err)
	}
	_, readErr := io.Copy(io.Discard, response.Body)
	closeErr := response.Body.Close()
	if readErr != nil {
		t.Fatalf("read readiness body: %v", readErr)
	}
	if closeErr != nil {
		t.Fatalf("close readiness body: %v", closeErr)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET readiness status = %d, want %d", response.StatusCode, http.StatusOK)
	}

	cancel()
	if err := waitForError(t, serveDone, "server shutdown"); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	if readiness.IsReady() {
		t.Fatal("readiness = true after server shutdown")
	}
}

func TestServerForcesCloseWhenGracefulShutdownExpires(t *testing.T) {
	t.Parallel()

	requestStarted := make(chan struct{})
	handlerDone := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /block", func(_ http.ResponseWriter, request *http.Request) {
		close(requestStarted)
		<-request.Context().Done()
		close(handlerDone)
	})

	configured := config.GatewayHTTPDefaults()
	configured.ShutdownTimeout = 25 * time.Millisecond
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}

	server := NewServer("test", configured, mux, health.NewState(), discardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- server.Serve(ctx, listener)
	}()
	waitForSignal(t, server.Started(), "server start")

	clientDone := make(chan error, 1)
	go func() {
		response, requestErr := (&http.Client{Timeout: time.Second}).Get("http://" + listener.Addr().String() + "/block")
		if response != nil {
			_ = response.Body.Close()
		}
		clientDone <- requestErr
	}()
	waitForSignal(t, requestStarted, "request start")

	cancel()
	shutdownErr := waitForError(t, serveDone, "forced shutdown")
	if !errors.Is(shutdownErr, context.DeadlineExceeded) {
		t.Fatalf("Serve() error = %v, want context deadline exceeded", shutdownErr)
	}
	waitForSignal(t, handlerDone, "handler cancellation")
	_ = waitForError(t, clientDone, "client completion")
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func waitForSignal(t *testing.T, signal <-chan struct{}, operation string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", operation)
	}
}

func waitForError(t *testing.T, result <-chan error, operation string) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", operation)
		return nil
	}
}
