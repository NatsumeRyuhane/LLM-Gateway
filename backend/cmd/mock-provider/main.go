package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/app"
	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/config"
	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/health"
	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/mockprovider"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := run(logger); err != nil {
		logger.Error("mock provider stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	configured, err := config.LoadHTTP("MOCK_PROVIDER", config.MockProviderHTTPDefaults(), os.LookupEnv)
	if err != nil {
		return fmt.Errorf("load mock-provider configuration: %w", err)
	}
	scenarioSettings, err := config.LoadMockProvider(config.MockProviderDefaults(), os.LookupEnv)
	if err != nil {
		return fmt.Errorf("load mock-provider scenario: %w", err)
	}
	providerHandler, err := newProviderHandler(scenarioSettings, logger)
	if err != nil {
		return fmt.Errorf("construct mock-provider scenario: %w", err)
	}

	readiness := health.NewState()
	mux := http.NewServeMux()
	readiness.Register(mux)
	mux.Handle(mockprovider.ChatCompletionsPath, providerHandler)
	server := app.NewServer("mock-provider", configured, mux, readiness, logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return server.Run(ctx)
}

func newProviderHandler(settings config.MockProvider, logger *slog.Logger) (http.Handler, error) {
	catalog, err := mockprovider.LoadCatalog()
	if err != nil {
		return nil, err
	}
	profile, ok := catalog.Profile(settings.Profile)
	if !ok {
		return nil, fmt.Errorf("mock-provider profile is unknown")
	}
	var scheduler mockprovider.Scheduler
	if profile.Behavior.Kind == mockprovider.BehaviorGatedStream {
		delays, delayErr := mockprovider.NewDelayScheduler(map[mockprovider.Event]time.Duration{
			mockprovider.EventResponseChunkReady: settings.StepDelay,
		})
		if delayErr != nil {
			return nil, delayErr
		}
		scheduler = delays
	}
	observer := mockprovider.ObserverFunc(func(observation mockprovider.Observation) {
		if logger == nil {
			return
		}
		logger.Debug("mock-provider lifecycle",
			"schema", observation.SchemaVersion,
			"profile", observation.ProfileID,
			"seed", observation.Seed,
			"ordinal", observation.RequestOrdinal,
			"event", observation.Event,
			"mode", observation.Mode,
			"ground_truth", observation.GroundTruth,
		)
	})
	scenario, err := mockprovider.NewScenario(catalog, settings.Profile, mockprovider.ScenarioOptions{Seed: settings.Seed, Scheduler: scheduler, Observer: observer})
	if err != nil {
		return nil, err
	}
	return mockprovider.NewHandler(scenario)
}
