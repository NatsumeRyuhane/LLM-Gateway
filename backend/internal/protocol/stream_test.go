package protocol

import (
	"slices"
	"testing"
	"testing/quick"
	"time"
)

func TestStreamValidatorAssemblesDistinctTextRefusalAndUsage(t *testing.T) {
	t.Parallel()

	validator := newTextStreamValidator(t)
	events := []CanonicalEvent{
		streamEvent(EventResponseStarted, 1),
		withText(streamEvent(EventOutputTextDelta, 2), "normal "),
		withText(streamEvent(EventOutputTextDelta, 3), "output"),
		withText(streamEvent(EventRefusalDelta, 4), "separate refusal"),
		withUsage(streamEvent(EventUsageUpdated, 5), CanonicalUsage{InputTokens: 4, OutputTokens: 2, TotalTokens: 6, Provenance: UsageProviderReported, Partial: false}),
		withFinish(streamEvent(EventResponseCompleted, 6), FinishStop),
	}
	acceptAll(t, validator, events)
	if err := validator.FinalizeEOF(); err != nil {
		t.Fatalf("FinalizeEOF() error = %v", err)
	}
	if !validator.Successful() || validator.State() != StreamCompleted {
		t.Fatalf("terminal state = %s, successful = %v", validator.State(), validator.Successful())
	}
	result, err := validator.Result()
	if err != nil {
		t.Fatalf("Result() error = %v", err)
	}
	if got := result.Message.Content[0].Text; got != "normal output" {
		t.Fatalf("content = %q", got)
	}
	if got, present := result.Message.Refusal.Get(); !present || got != "separate refusal" {
		t.Fatalf("refusal = (%q, %v)", got, present)
	}
}

func TestStreamValidatorRequiresCanonicalTerminalEvent(t *testing.T) {
	t.Parallel()

	validator := newTextStreamValidator(t)
	acceptAll(t, validator, []CanonicalEvent{
		streamEvent(EventResponseStarted, 1),
		withText(streamEvent(EventOutputTextDelta, 2), "partial"),
	})
	err := validator.FinalizeEOF()
	if err == nil || err.Code != FailureProtocolEarlyEOF || err.RetryDisposition != RetryClientDecides || !err.OutputVisible {
		t.Fatalf("FinalizeEOF() error = %#v", err)
	}
	if validator.Successful() {
		t.Fatal("EOF without response.completed counted as success")
	}
}

func TestStreamValidatorAssemblesOrderedInterleavedToolCalls(t *testing.T) {
	t.Parallel()

	requestValue := validRequest()
	requestValue.Stream = true
	requestValue.Tools = []CanonicalFunctionTool{
		{Name: "first", Parameters: NewJSONSchema([]byte(`{"type":"object","properties":{"value":{"type":"string"}},"required":["value"],"additionalProperties":false}`))},
		{Name: "second", Parameters: NewJSONSchema([]byte(`{"type":"object","properties":{"value":{"type":"string"}},"required":["value"],"additionalProperties":false}`))},
	}
	requestValue.ToolChoice = ToolChoice{Kind: ToolChoiceAuto}
	requestValue.ParallelToolCalls = Some(true)
	request := mustValidateRequest(t, requestValue)
	validator, err := NewStreamValidator(request, "attempt-1", "route-1")
	if err != nil {
		t.Fatalf("NewStreamValidator() error = %v", err)
	}
	emoji := []byte("😀")
	firstFragment := `{"value":"` + string(emoji[:2])
	secondFragment := string(emoji[2:]) + `"}`
	events := []CanonicalEvent{
		streamEvent(EventResponseStarted, 1),
		withToolStart(streamEvent(EventToolCallStarted, 2), 0, "call-0", "first"),
		withToolStart(streamEvent(EventToolCallStarted, 3), 1, "call-1", "second"),
		withToolArguments(streamEvent(EventToolCallArgumentsDelta, 4), 0, firstFragment),
		withToolArguments(streamEvent(EventToolCallArgumentsDelta, 5), 1, `{"value":"two"}`),
		withToolArguments(streamEvent(EventToolCallArgumentsDelta, 6), 0, secondFragment),
		withToolCompleted(streamEvent(EventToolCallCompleted, 7), 1),
		withToolCompleted(streamEvent(EventToolCallCompleted, 8), 0),
		withFinish(streamEvent(EventResponseCompleted, 9), FinishToolCalls),
	}
	acceptAll(t, validator, events)
	result, resultErr := validator.Result()
	if resultErr != nil {
		t.Fatalf("Result() error = %v", resultErr)
	}
	want := []CanonicalToolCall{
		{ID: "call-0", Name: "first", Arguments: `{"value":"😀"}`},
		{ID: "call-1", Name: "second", Arguments: `{"value":"two"}`},
	}
	if !slices.Equal(result.Message.ToolCalls, want) {
		t.Fatalf("tool calls = %#v, want %#v", result.Message.ToolCalls, want)
	}
}

func TestStreamValidatorRejectsInvalidTransitionsAndMonotonicity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		run  func(*testing.T, *StreamValidator) *CanonicalError
		code FailureCode
	}{
		{"delta before start", func(_ *testing.T, v *StreamValidator) *CanonicalError {
			return v.Accept(withText(streamEvent(EventOutputTextDelta, 1), "bad"))
		}, FailureProtocolInvalidEventOrder},
		{"duplicate sequence", func(t *testing.T, v *StreamValidator) *CanonicalError {
			acceptAll(t, v, []CanonicalEvent{streamEvent(EventResponseStarted, 1), withText(streamEvent(EventOutputTextDelta, 2), "ok")})
			return v.Accept(withText(streamEvent(EventRefusalDelta, 2), "bad"))
		}, FailureProtocolInvalidEventOrder},
		{"timestamp regression", func(t *testing.T, v *StreamValidator) *CanonicalError {
			acceptAll(t, v, []CanonicalEvent{streamEvent(EventResponseStarted, 1)})
			event := withText(streamEvent(EventOutputTextDelta, 2), "bad")
			event.ObservedAt = event.ObservedAt.Add(-time.Hour)
			return v.Accept(event)
		}, FailureProtocolInvalidEventOrder},
		{"usage regression", func(t *testing.T, v *StreamValidator) *CanonicalError {
			acceptAll(t, v, []CanonicalEvent{
				streamEvent(EventResponseStarted, 1),
				withText(streamEvent(EventOutputTextDelta, 2), "ok"),
				withUsage(streamEvent(EventUsageUpdated, 3), CanonicalUsage{InputTokens: 2, OutputTokens: 2, TotalTokens: 4, Provenance: UsageProviderReported, Partial: true}),
			})
			return v.Accept(withUsage(streamEvent(EventUsageUpdated, 4), CanonicalUsage{InputTokens: 2, OutputTokens: 1, TotalTokens: 3, Provenance: UsageProviderReported}))
		}, FailureProtocolUsageInconsistent},
		{"unknown finish", func(t *testing.T, v *StreamValidator) *CanonicalError {
			acceptAll(t, v, []CanonicalEvent{streamEvent(EventResponseStarted, 1), withText(streamEvent(EventOutputTextDelta, 2), "ok")})
			return v.Accept(withFinish(streamEvent(EventResponseCompleted, 3), "unknown"))
		}, FailureProtocolInvalidEventOrder},
		{"event after terminal", func(t *testing.T, v *StreamValidator) *CanonicalError {
			acceptAll(t, v, []CanonicalEvent{streamEvent(EventResponseStarted, 1), withText(streamEvent(EventOutputTextDelta, 2), "ok"), withFinish(streamEvent(EventResponseCompleted, 3), FinishStop)})
			return v.Accept(withText(streamEvent(EventOutputTextDelta, 4), "late"))
		}, FailureProtocolInvalidEventOrder},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := test.run(t, newTextStreamValidator(t))
			if err == nil || err.Code != test.code {
				t.Fatalf("error = %#v, want %s", err, test.code)
			}
		})
	}
}

func TestStreamValidatorRejectsIncompleteAndInvalidToolCalls(t *testing.T) {
	t.Parallel()

	newValidator := func(t *testing.T) *StreamValidator {
		requestValue := validRequest()
		requestValue.Stream = true
		requestValue.Tools = []CanonicalFunctionTool{{Name: "tool", Parameters: NewJSONSchema([]byte(`{"type":"object","required":["value"],"properties":{"value":{"type":"string"}},"additionalProperties":false}`))}}
		requestValue.ToolChoice = ToolChoice{Kind: ToolChoiceAuto}
		request := mustValidateRequest(t, requestValue)
		validator, err := NewStreamValidator(request, "attempt-1", "route-1")
		if err != nil {
			t.Fatalf("NewStreamValidator() error = %v", err)
		}
		return validator
	}

	t.Run("incomplete", func(t *testing.T) {
		validator := newValidator(t)
		acceptAll(t, validator, []CanonicalEvent{
			streamEvent(EventResponseStarted, 1),
			withToolStart(streamEvent(EventToolCallStarted, 2), 0, "call-1", "tool"),
			withToolArguments(streamEvent(EventToolCallArgumentsDelta, 3), 0, `{"value":"ok"}`),
		})
		err := validator.Accept(withFinish(streamEvent(EventResponseCompleted, 4), FinishToolCalls))
		if err == nil || err.Code != FailureProtocolInvalidToolCall {
			t.Fatalf("error = %#v", err)
		}
	})

	t.Run("schema", func(t *testing.T) {
		validator := newValidator(t)
		acceptAll(t, validator, []CanonicalEvent{
			streamEvent(EventResponseStarted, 1),
			withToolStart(streamEvent(EventToolCallStarted, 2), 0, "call-1", "tool"),
			withToolArguments(streamEvent(EventToolCallArgumentsDelta, 3), 0, `{"wrong":true}`),
		})
		err := validator.Accept(withToolCompleted(streamEvent(EventToolCallCompleted, 4), 0))
		if err == nil || err.Code != FailureProtocolInvalidToolCall || err.RetryDisposition != RetryClientDecides {
			t.Fatalf("error = %#v", err)
		}
	})
}

func TestStreamValidatorAcceptsExplicitFailedTerminalWithoutCallingItSuccess(t *testing.T) {
	t.Parallel()

	validator := newTextStreamValidator(t)
	acceptAll(t, validator, []CanonicalEvent{
		streamEvent(EventResponseStarted, 1),
		withText(streamEvent(EventOutputTextDelta, 2), "partial"),
	})
	failure := &CanonicalError{
		Code: FailureUpstreamStreamStalled, Domain: DomainUpstream,
		RetryDisposition: RetryClientDecides, SafeMessage: "The upstream stream stalled.", HTTPStatus: 502,
		RequestID: "request-1", OutputVisible: true,
	}
	event := streamEvent(EventResponseFailed, 3)
	event.Failure = failure
	if err := validator.Accept(event); err != nil {
		t.Fatalf("Accept(response.failed) error = %v", err)
	}
	if err := validator.FinalizeEOF(); err != nil {
		t.Fatalf("FinalizeEOF() error = %v", err)
	}
	if validator.Successful() || validator.State() != StreamFailed {
		t.Fatalf("state = %s, successful = %v", validator.State(), validator.Successful())
	}
	_, resultErr := validator.Result()
	if resultErr == nil || resultErr.Code != FailureUpstreamStreamStalled {
		t.Fatalf("Result() error = %#v", resultErr)
	}
}

func TestStreamValidatorPoisonsAfterFirstRejectedEvent(t *testing.T) {
	t.Parallel()

	validator := newTextStreamValidator(t)
	first := validator.Accept(withText(streamEvent(EventOutputTextDelta, 1), "before start"))
	if first == nil || first.Code != FailureProtocolInvalidEventOrder {
		t.Fatalf("first error = %#v", first)
	}
	if validator.State() != StreamFailed || validator.Successful() {
		t.Fatalf("state = %s, successful = %v", validator.State(), validator.Successful())
	}
	for _, event := range []CanonicalEvent{
		streamEvent(EventResponseStarted, 2),
		withText(streamEvent(EventOutputTextDelta, 3), "ignored"),
		withFinish(streamEvent(EventResponseCompleted, 4), FinishStop),
	} {
		err := validator.Accept(event)
		if err == nil || err.Error() != first.Error() {
			t.Fatalf("Accept(%s) error = %#v, want first rejection", event.Type, err)
		}
	}
	if err := validator.FinalizeEOF(); err == nil || err.Error() != first.Error() {
		t.Fatalf("FinalizeEOF() error = %#v, want first rejection", err)
	}
	if validator.Successful() {
		t.Fatal("rejected stream became successful")
	}
}

func TestInternalStreamFailureUsesGatewayEnvelope(t *testing.T) {
	t.Parallel()

	var validator *StreamValidator
	err := validator.Accept(CanonicalEvent{})
	if err == nil || err.Code != FailureGatewayInternal || err.Domain != DomainGateway || err.HTTPStatus != 500 {
		t.Fatalf("nil validator error = %#v", err)
	}
}

func TestStreamSequenceMonotonicProperty(t *testing.T) {
	t.Parallel()

	property := func(first, second uint16) bool {
		validator := newTextStreamValidator(t)
		if err := validator.Accept(streamEvent(EventResponseStarted, 1)); err != nil {
			return false
		}
		firstSequence := uint64(first) + 2
		if err := validator.Accept(withText(streamEvent(EventOutputTextDelta, firstSequence), "x")); err != nil {
			return false
		}
		secondSequence := uint64(second) + 2
		err := validator.Accept(withUsage(streamEvent(EventUsageUpdated, secondSequence), CanonicalUsage{Provenance: UsageUnavailable, Partial: true}))
		return (err == nil) == (secondSequence > firstSequence)
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 250}); err != nil {
		t.Fatal(err)
	}
}

func FuzzStreamValidatorEOFNeverImpliesSuccess(f *testing.F) {
	f.Add("partial", uint64(2))
	f.Add("", uint64(0))
	f.Fuzz(func(t *testing.T, text string, sequence uint64) {
		validator := newTextStreamValidator(t)
		_ = validator.Accept(streamEvent(EventResponseStarted, 1))
		event := withText(streamEvent(EventOutputTextDelta, sequence), text)
		_ = validator.Accept(event)
		_ = validator.FinalizeEOF()
		if validator.Successful() {
			t.Fatal("stream without response.completed became successful")
		}
	})
}

func newTextStreamValidator(t *testing.T) *StreamValidator {
	t.Helper()
	requestValue := validRequest()
	requestValue.Stream = true
	request := mustValidateRequest(t, requestValue)
	validator, err := NewStreamValidator(request, "attempt-1", "route-1")
	if err != nil {
		t.Fatalf("NewStreamValidator() error = %v", err)
	}
	return validator
}

func streamEvent(eventType EventType, sequence uint64) CanonicalEvent {
	return CanonicalEvent{
		Type: eventType, RequestID: "request-1", AttemptID: "attempt-1", RouteID: "route-1",
		Sequence: sequence, ObservedAt: time.Unix(1_800_000_000+int64(sequence), 0),
	}
}

func withText(event CanonicalEvent, text string) CanonicalEvent {
	event.Text = text
	return event
}

func withUsage(event CanonicalEvent, usage CanonicalUsage) CanonicalEvent {
	event.Usage = Some(usage)
	return event
}

func withFinish(event CanonicalEvent, finish FinishReason) CanonicalEvent {
	event.FinishReason = finish
	return event
}

func withToolStart(event CanonicalEvent, index int, id, name string) CanonicalEvent {
	event.ToolCallIndex = index
	event.ToolCallID = id
	event.ToolName = name
	return event
}

func withToolArguments(event CanonicalEvent, index int, text string) CanonicalEvent {
	event.ToolCallIndex = index
	event.Text = text
	return event
}

func withToolCompleted(event CanonicalEvent, index int) CanonicalEvent {
	event.ToolCallIndex = index
	return event
}

func acceptAll(t *testing.T, validator *StreamValidator, events []CanonicalEvent) {
	t.Helper()
	for _, event := range events {
		if err := validator.Accept(event); err != nil {
			t.Fatalf("Accept(%s, sequence %d) error = %v", event.Type, event.Sequence, err)
		}
	}
}
