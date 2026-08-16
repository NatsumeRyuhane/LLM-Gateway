package mockprovider

import (
	"context"
	"errors"
	"sync/atomic"
	"time"
)

const ObservationSchemaVersion = "gateway.mock-provider.observation.v0"

// Event is a bounded mock-provider request lifecycle event.
type Event string

const (
	EventRequestReceived       Event = "request.received"
	EventResponseHeadersReady  Event = "response.headers_ready"
	EventResponseChunkReady    Event = "response.chunk_ready"
	EventResponseTerminalReady Event = "response.terminal_ready"
	EventRequestCancelled      Event = "request.cancelled"
)

func (e Event) valid() bool {
	switch e {
	case EventRequestReceived, EventResponseHeadersReady, EventResponseChunkReady, EventResponseTerminalReady, EventRequestCancelled:
		return true
	default:
		return false
	}
}

// Observation contains only bounded synthetic lifecycle metadata.
type Observation struct {
	SchemaVersion  string
	ProfileID      string
	Seed           int64
	RequestOrdinal uint64
	Event          Event
	Mode           Mode
	GroundTruth    string
}

// Observer receives metadata-only lifecycle observations.
type Observer interface {
	Observe(Observation)
}

// ObserverFunc adapts a function to Observer.
type ObserverFunc func(Observation)

// Observe implements Observer.
func (function ObserverFunc) Observe(observation Observation) { function(observation) }

// Scheduler controls lifecycle progress. Wait must stop when ctx ends.
type Scheduler interface {
	Wait(context.Context, Event) error
}

// SchedulerFunc adapts a function to Scheduler.
type SchedulerFunc func(context.Context, Event) error

// Wait implements Scheduler.
func (function SchedulerFunc) Wait(ctx context.Context, event Event) error {
	return function(ctx, event)
}

type immediateScheduler struct{}

func (immediateScheduler) Wait(context.Context, Event) error { return nil }

// DelayScheduler applies bounded real delays to selected lifecycle events.
type DelayScheduler struct {
	delays map[Event]time.Duration
}

// NewDelayScheduler snapshots validated event delays.
func NewDelayScheduler(delays map[Event]time.Duration) (DelayScheduler, error) {
	cloned := make(map[Event]time.Duration, len(delays))
	for event, delay := range delays {
		if !event.valid() || delay < 0 || delay > time.Minute {
			return DelayScheduler{}, errors.New("mock-provider delay is invalid")
		}
		cloned[event] = delay
	}
	return DelayScheduler{delays: cloned}, nil
}

// Wait waits for the configured delay or context cancellation.
func (s DelayScheduler) Wait(ctx context.Context, event Event) error {
	delay := s.delays[event]
	if delay == 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ScenarioOptions controls one isolated scenario instance.
type ScenarioOptions struct {
	Seed      int64
	Scheduler Scheduler
	Observer  Observer
}

// Scenario owns all state for one profile execution domain.
type Scenario struct {
	profile   Profile
	seed      int64
	scheduler Scheduler
	observer  Observer
	ordinal   atomic.Uint64
}

// NewScenario creates an isolated provider-handler scenario.
func NewScenario(catalog Catalog, profileID string, options ScenarioOptions) (*Scenario, error) {
	if profileID == "" {
		profileID = catalog.DefaultProfile()
	}
	profile, ok := catalog.Profile(profileID)
	if !ok {
		return nil, errors.New("mock-provider profile is unknown")
	}
	if profile.InjectionLayer != LayerProviderHandler || profile.Mode == ModeExternal {
		return nil, errors.New("mock-provider profile requires an external harness")
	}
	seed := options.Seed
	if seed == 0 {
		seed = profile.Seed
	}
	scheduler := options.Scheduler
	if scheduler == nil {
		scheduler = immediateScheduler{}
	}
	return &Scenario{profile: profile, seed: seed, scheduler: scheduler, observer: options.Observer}, nil
}

// Profile returns an isolated copy of the scenario profile.
func (s *Scenario) Profile() Profile {
	if s == nil {
		return Profile{}
	}
	return cloneProfile(s.profile)
}

func (s *Scenario) begin(mode Mode) requestState {
	state := requestState{scenario: s, ordinal: s.ordinal.Add(1), mode: mode}
	state.observe(EventRequestReceived)
	return state
}

type requestState struct {
	scenario  *Scenario
	ordinal   uint64
	mode      Mode
	cancelled bool
}

func (s *requestState) reach(ctx context.Context, event Event) error {
	if err := s.scenario.scheduler.Wait(ctx, event); err != nil {
		s.cancel()
		return err
	}
	s.observe(event)
	return nil
}

func (s *requestState) cancel() {
	if s.cancelled {
		return
	}
	s.cancelled = true
	s.observe(EventRequestCancelled)
}

func (s *requestState) observe(event Event) {
	if s.scenario.observer == nil {
		return
	}
	s.scenario.observer.Observe(Observation{
		SchemaVersion:  ObservationSchemaVersion,
		ProfileID:      s.scenario.profile.ID,
		Seed:           s.scenario.seed,
		RequestOrdinal: s.ordinal,
		Event:          event,
		Mode:           s.mode,
		GroundTruth:    s.scenario.profile.GroundTruth,
	})
}
