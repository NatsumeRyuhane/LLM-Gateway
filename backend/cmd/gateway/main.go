package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/app"
	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/config"
	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/health"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := run(logger); err != nil {
		logger.Error("gateway stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	configured, err := config.LoadHTTP("GATEWAY", config.GatewayHTTPDefaults(), os.LookupEnv)
	if err != nil {
		return fmt.Errorf("load gateway configuration: %w", err)
	}

	readiness := health.NewState()
	mux := http.NewServeMux()
	readiness.Register(mux)
	server := app.NewServer("gateway", configured, mux, readiness, logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return server.Run(ctx)
}
