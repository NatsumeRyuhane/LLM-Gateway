package openai

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/protocol"
)

func TestStreamEncoderGoldenConformance(t *testing.T) {
	request := mustDecodeStreamingRequest(t)
	encoder := mustStreamEncoder(t, request)
	events := []protocol.CanonicalEvent{
		streamEvent(protocol.EventResponseStarted, 1),
		withText(streamEvent(protocol.EventOutputTextDelta, 2), "hello"),
		withText(streamEvent(protocol.EventRefusalDelta, 3), "cannot"),
		withToolStart(streamEvent(protocol.EventToolCallStarted, 4), 0, "call_1", "lookup"),
		withToolArguments(streamEvent(protocol.EventToolCallArgumentsDelta, 5), 0, `{"query":"x"}`),
		withToolIndex(streamEvent(protocol.EventToolCallCompleted, 6), 0),
		withUsage(streamEvent(protocol.EventUsageUpdated, 7), protocol.CanonicalUsage{InputTokens: 12, OutputTokens: 4, TotalTokens: 16, Provenance: protocol.UsageProviderReported}),
		withFinish(streamEvent(protocol.EventResponseCompleted, 8), protocol.FinishToolCalls),
	}
	var output bytes.Buffer
	for _, event := range events {
		frames, err := encoder.Encode(event)
		if err != nil {
			t.Fatalf("Encode(%s) error = %v", event.Type, err)
		}
		output.Write(frames)
	}
	if !encoder.Successful() || encoder.FinalizeEOF() != nil {
		t.Fatal("stream did not reach canonical success")
	}
	if encoder.Header().Get(HeaderAttemptID) != "attempt_1" || encoder.Header().Get(HeaderRouteID) != "route_1" {
		t.Fatalf("stream headers = %#v", encoder.Header())
	}

	data, err := os.ReadFile(filepath.Join("..", "..", "..", "tests", "conformance", "gateway.adapter.v0", "http", "stream-response.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		ContractVersion string `json:"contract_version"`
		ExpectedSSE     string `json:"expected_sse"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.ContractVersion != protocol.ContractVersion || output.String() != fixture.ExpectedSSE {
		t.Fatalf("SSE output mismatch\n got: %q\nwant: %q", output.String(), fixture.ExpectedSSE)
	}
}

func TestStreamEncoderRejectsMixedAttemptsBeforeSerialization(t *testing.T) {
	encoder := mustStreamEncoder(t, mustDecodeStreamingRequest(t))
	if frames, err := encoder.Encode(streamEvent(protocol.EventResponseStarted, 1)); err != nil || len(frames) != 0 {
		t.Fatalf("start = %q, %v", frames, err)
	}
	event := withText(streamEvent(protocol.EventOutputTextDelta, 2), "secret")
	event.AttemptID = "attempt_2"
	frames, err := encoder.Encode(event)
	if err == nil || err.Code != protocol.FailureProtocolInvalidEventOrder || len(frames) != 0 {
		t.Fatalf("mixed attempt = %q, %#v", frames, err)
	}
	valid := withText(streamEvent(protocol.EventOutputTextDelta, 2), "must-not-appear")
	frames, poisonedErr := encoder.Encode(valid)
	if poisonedErr == nil || len(frames) != 0 || encoder.Successful() {
		t.Fatalf("poisoned encoder = %q, %#v", frames, poisonedErr)
	}
}

func TestStreamFailureNeverEmitsDone(t *testing.T) {
	encoder := mustStreamEncoder(t, mustDecodeStreamingRequest(t))
	_, _ = encoder.Encode(streamEvent(protocol.EventResponseStarted, 1))
	visible, err := encoder.Encode(withText(streamEvent(protocol.EventOutputTextDelta, 2), "partial"))
	if err != nil || len(visible) == 0 {
		t.Fatalf("visible output = %q, %v", visible, err)
	}
	failure := &protocol.CanonicalError{
		Code: protocol.FailureUpstreamStreamStalled, Domain: protocol.DomainUpstream,
		RetryDisposition: protocol.RetryClientDecides, SafeMessage: "The upstream stream stalled.", HTTPStatus: 502,
		RequestID: testMetadata.RequestID, AttemptID: "attempt_1", RouteID: "route_1", OutputVisible: true,
	}
	failedEvent := streamEvent(protocol.EventResponseFailed, 3)
	failedEvent.Failure = failure
	frames, terminalErr := encoder.Encode(failedEvent)
	if terminalErr == nil || terminalErr.Code != protocol.FailureUpstreamStreamStalled || len(frames) != 0 || bytes.Contains(frames, []byte("[DONE]")) {
		t.Fatalf("failure result = %q, %#v", frames, terminalErr)
	}
	if encoder.Successful() || encoder.FinalizeEOF() != nil {
		t.Fatal("failed stream reported success or non-terminal EOF")
	}
}

func TestStreamEarlyEOFNeverEmitsDone(t *testing.T) {
	encoder := mustStreamEncoder(t, mustDecodeStreamingRequest(t))
	_, _ = encoder.Encode(streamEvent(protocol.EventResponseStarted, 1))
	frames, err := encoder.Encode(withText(streamEvent(protocol.EventOutputTextDelta, 2), "partial"))
	if err != nil || bytes.Contains(frames, []byte("[DONE]")) {
		t.Fatalf("delta = %q, %v", frames, err)
	}
	if eofErr := encoder.FinalizeEOF(); eofErr == nil || eofErr.Code != protocol.FailureProtocolEarlyEOF || bytes.Contains(frames, []byte("[DONE]")) {
		t.Fatalf("FinalizeEOF() = %#v", eofErr)
	}
}

func FuzzStreamEncoderSerializationBounds(f *testing.F) {
	f.Add("hello")
	f.Add(string([]byte{0xff, 0xfe}))
	request := mustDecodeStreamingRequest(f)
	f.Fuzz(func(t *testing.T, text string) {
		encoder := mustStreamEncoder(t, request)
		_, _ = encoder.Encode(streamEvent(protocol.EventResponseStarted, 1))
		frames, err := encoder.Encode(withText(streamEvent(protocol.EventOutputTextDelta, 2), text))
		if err != nil && len(frames) != 0 {
			t.Fatalf("rejected event emitted %q", frames)
		}
		if bytes.Contains(frames, []byte("[DONE]")) {
			t.Fatalf("non-terminal delta emitted success sentinel: %q", frames)
		}
	})
}

func mustDecodeStreamingRequest(t testing.TB) protocol.ValidatedChatRequest {
	t.Helper()
	request := newChatRequest(`{
      "model":"agent","messages":[{"role":"user","content":"hello"}],
      "stream":true,"stream_options":{"include_usage":true},
      "tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"],"additionalProperties":false}}}]
    }`)
	request.Header.Set("Accept", "text/event-stream")
	validated, err := NewCodec(protocol.DefaultLimits()).DecodeChatCompletions(request, testMetadata)
	if err != nil {
		t.Fatalf("DecodeChatCompletions() error = %v", err)
	}
	return validated
}

func mustStreamEncoder(t testing.TB, request protocol.ValidatedChatRequest) *StreamEncoder {
	t.Helper()
	encoder, err := NewCodec(protocol.DefaultLimits()).NewStreamEncoder(
		request, "attempt_1", "route_1", StreamMetadata{ResponseID: "resp_stream", CreatedAt: time.Unix(1_700_000_000, 0)},
		CorrelationVisibility{AttemptID: true, RouteID: true},
	)
	if err != nil {
		t.Fatalf("NewStreamEncoder() error = %v", err)
	}
	return encoder
}

func streamEvent(kind protocol.EventType, sequence uint64) protocol.CanonicalEvent {
	return protocol.CanonicalEvent{
		Type: kind, RequestID: testMetadata.RequestID, AttemptID: "attempt_1", RouteID: "route_1",
		Sequence: sequence, ObservedAt: time.Unix(1_700_000_000, int64(sequence)),
	}
}

func withText(event protocol.CanonicalEvent, text string) protocol.CanonicalEvent {
	event.Text = text
	return event
}

func withToolStart(event protocol.CanonicalEvent, index int, id, name string) protocol.CanonicalEvent {
	event.ToolCallIndex = index
	event.ToolCallID = id
	event.ToolName = name
	return event
}

func withToolArguments(event protocol.CanonicalEvent, index int, arguments string) protocol.CanonicalEvent {
	event.ToolCallIndex = index
	event.Text = arguments
	return event
}

func withToolIndex(event protocol.CanonicalEvent, index int) protocol.CanonicalEvent {
	event.ToolCallIndex = index
	return event
}

func withUsage(event protocol.CanonicalEvent, usage protocol.CanonicalUsage) protocol.CanonicalEvent {
	event.Usage = protocol.Some(usage)
	return event
}

func withFinish(event protocol.CanonicalEvent, finish protocol.FinishReason) protocol.CanonicalEvent {
	event.FinishReason = finish
	return event
}
