package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/protocol"
	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/provider"
)

// Stream incrementally parses one upstream SSE generation into canonical
// events and returns only after canonical terminal validation.
func (a *Adapter) Stream(ctx context.Context, attempt provider.Attempt, request protocol.ValidatedChatRequest, route provider.ValidatedRoute, sink provider.EventSink) (protocol.StreamResult, *protocol.CanonicalError) {
	if sink == nil {
		return protocol.StreamResult{}, attachAttempt(failure(protocol.FailureGatewayInternal, protocol.DomainGateway, protocol.RetryNever, http.StatusInternalServerError, "The stream consumer is unavailable."), request, attempt, route)
	}
	if preflightFailure := preflight(attempt, request, route, true); preflightFailure != nil {
		return protocol.StreamResult{}, attachAttempt(preflightFailure, request, attempt, route)
	}
	body, err := json.Marshal(translateRequest(request, route.UpstreamModel()))
	if err != nil {
		return protocol.StreamResult{}, attachAttempt(failure(protocol.FailureGatewayInternal, protocol.DomainGateway, protocol.RetryNever, http.StatusInternalServerError, "The gateway could not encode the upstream request."), request, attempt, route)
	}
	requestCtx, cancel := requestContext(ctx, request)
	defer cancel()
	endpoint := route.Endpoint()
	outbound, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return protocol.StreamResult{}, attachAttempt(failure(protocol.FailureGatewayInternal, protocol.DomainGateway, protocol.RetryNever, http.StatusInternalServerError, "The gateway could not create the upstream request."), request, attempt, route)
	}
	setOutboundHeaders(outbound.Header)
	outbound.Header.Set("Accept", "text/event-stream")
	if err := route.ApplyCredential(outbound.Header); err != nil {
		return protocol.StreamResult{}, attachAttempt(failure(protocol.FailureGatewayInternal, protocol.DomainGateway, protocol.RetryNever, http.StatusInternalServerError, "The provider route credential is unavailable."), request, attempt, route)
	}
	response, err := a.client.Do(outbound)
	if err != nil {
		return protocol.StreamResult{}, attachAttempt(classifyTransport(err), request, attempt, route)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return protocol.StreamResult{}, attachAttempt(classifyResponseStatus(response, route.Limits().MaxErrorBodyBytes), request, attempt, route)
	}
	if !sseContentType(response.Header.Get("Content-Type")) {
		return protocol.StreamResult{}, attachAttempt(protocolFailure(protocol.FailureProtocolInvalidSSE, "The upstream stream is invalid.", "response.content_type", "must be text/event-stream"), request, attempt, route)
	}

	validator, validationFailure := protocol.NewStreamValidator(request, attempt.ID, route.ID())
	if validationFailure != nil {
		return protocol.StreamResult{}, attachAttempt(validationFailure, request, attempt, route)
	}
	parser := streamParser{adapter: a, request: request, attempt: attempt, route: route, validator: validator, sink: sink}
	parseFailure := parser.parse(response.Body)
	if parseFailure != nil {
		return protocol.StreamResult{}, attachAttempt(parseFailure, request, attempt, route)
	}
	result, resultFailure := validator.Result()
	return result, attachAttempt(resultFailure, request, attempt, route)
}

type streamParser struct {
	adapter   *Adapter
	request   protocol.ValidatedChatRequest
	attempt   provider.Attempt
	route     provider.ValidatedRoute
	validator *protocol.StreamValidator
	sink      provider.EventSink

	sequence    uint64
	started     bool
	providerID  string
	finish      protocol.FinishReason
	finished    bool
	usageSeen   bool
	toolStarted map[int]bool
}

func (p *streamParser) parse(reader io.Reader) *protocol.CanonicalError {
	limits := p.route.Limits()
	scanner := bufio.NewScanner(reader)
	initialBuffer := min(64<<10, limits.MaxSSELineBytes)
	scanner.Buffer(make([]byte, initialBuffer), limits.MaxSSELineBytes)
	var data strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if data.Len() == 0 {
				continue
			}
			if parseFailure := p.consumeData(data.String()); parseFailure != nil {
				return parseFailure
			}
			data.Reset()
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, found := strings.Cut(line, ":")
		if found && strings.HasPrefix(value, " ") {
			value = value[1:]
		}
		if field != "data" {
			return p.invalidSSE("stream.field", "only data fields and comments are accepted")
		}
		if data.Len() > 0 {
			data.WriteByte('\n')
		}
		if data.Len()+len(value) > limits.MaxSSEEventBytes {
			return p.tooLarge("stream.event", "exceeds the SSE event bound")
		}
		data.WriteString(value)
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return p.tooLarge("stream.line", "exceeds the SSE line bound")
		}
		return p.withVisibility(classifyTransport(err))
	}
	if data.Len() > 0 {
		if parseFailure := p.consumeData(data.String()); parseFailure != nil {
			return parseFailure
		}
	}
	return p.validator.FinalizeEOF()
}

func (p *streamParser) consumeData(data string) *protocol.CanonicalError {
	if data == "[DONE]" {
		if !p.finished {
			return p.invalidSSE("stream.done", "requires a prior finish reason")
		}
		return p.emit(protocol.CanonicalEvent{Type: protocol.EventResponseCompleted, FinishReason: p.finish})
	}
	if p.validator.Successful() {
		return p.invalidSSE("stream", "no event may follow [DONE]")
	}
	if !utf8.ValidString(data) {
		return p.invalidSSE("stream.data", "must be valid UTF-8")
	}
	var chunk chatChunk
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		return p.invalidJSON("stream.data", "must be a valid Chat Completions chunk")
	}
	if chunk.Object != "chat.completion.chunk" || chunk.ID == "" || chunk.Created <= 0 || chunk.Model == "" {
		return p.invalidJSON("stream.chunk", "must contain required chunk metadata")
	}
	if p.providerID == "" {
		p.providerID = chunk.ID
	} else if p.providerID != chunk.ID {
		return p.invalidJSON("stream.id", "must remain stable")
	}
	if !p.started {
		if emitFailure := p.emit(protocol.CanonicalEvent{Type: protocol.EventResponseStarted}); emitFailure != nil {
			return emitFailure
		}
		p.started = true
	}
	if len(chunk.Choices) == 0 {
		if chunk.Usage == nil || !p.finished {
			return p.invalidJSON("stream.choices", "may be empty only for final usage after a finish reason")
		}
		return p.emitUsage(*chunk.Usage)
	}
	if len(chunk.Choices) != 1 || chunk.Choices[0].Index != 0 || p.finished {
		return p.invalidJSON("stream.choices", "must contain exactly one ordered unfinished choice")
	}
	choice := chunk.Choices[0]
	if choice.Delta.Role != "" && choice.Delta.Role != "assistant" {
		return p.invalidJSON("stream.delta.role", "must be assistant when present")
	}
	if choice.Delta.Content != nil && *choice.Delta.Content != "" {
		if emitFailure := p.emit(protocol.CanonicalEvent{Type: protocol.EventOutputTextDelta, Text: *choice.Delta.Content}); emitFailure != nil {
			return emitFailure
		}
	}
	if choice.Delta.Refusal != nil && *choice.Delta.Refusal != "" {
		if emitFailure := p.emit(protocol.CanonicalEvent{Type: protocol.EventRefusalDelta, Text: *choice.Delta.Refusal}); emitFailure != nil {
			return emitFailure
		}
	}
	if toolFailure := p.consumeTools(choice.Delta.ToolCalls); toolFailure != nil {
		return toolFailure
	}
	if chunk.Usage != nil {
		if usageFailure := p.emitUsage(*chunk.Usage); usageFailure != nil {
			return usageFailure
		}
	}
	if choice.FinishReason != nil {
		p.finish = finishReason(*choice.FinishReason)
		for index := 0; index < len(p.toolStarted); index++ {
			if emitFailure := p.emit(protocol.CanonicalEvent{Type: protocol.EventToolCallCompleted, ToolCallIndex: index}); emitFailure != nil {
				return emitFailure
			}
		}
		p.finished = true
	}
	return nil
}

func (p *streamParser) consumeTools(calls []deltaToolCall) *protocol.CanonicalError {
	if p.toolStarted == nil {
		p.toolStarted = make(map[int]bool)
	}
	for _, call := range calls {
		if call.Index < 0 {
			return p.invalidJSON("stream.delta.tool_calls.index", "must be non-negative")
		}
		if !p.toolStarted[call.Index] {
			if call.Index != len(p.toolStarted) || call.ID == "" || call.Type != "function" || call.Function.Name == "" {
				return p.invalidJSON("stream.delta.tool_calls", "must start in increasing order with ID, type, and function name")
			}
			if emitFailure := p.emit(protocol.CanonicalEvent{Type: protocol.EventToolCallStarted, ToolCallIndex: call.Index, ToolCallID: call.ID, ToolName: call.Function.Name}); emitFailure != nil {
				return emitFailure
			}
			p.toolStarted[call.Index] = true
		} else if call.ID != "" || call.Type != "" || call.Function.Name != "" {
			return p.invalidJSON("stream.delta.tool_calls", "must not restart an existing tool call")
		}
		if call.Function.Arguments != "" {
			if emitFailure := p.emit(protocol.CanonicalEvent{Type: protocol.EventToolCallArgumentsDelta, ToolCallIndex: call.Index, Text: call.Function.Arguments}); emitFailure != nil {
				return emitFailure
			}
		}
	}
	return nil
}

func (p *streamParser) emitUsage(value usage) *protocol.CanonicalError {
	if p.usageSeen {
		return p.invalidJSON("stream.usage", "must be reported at most once")
	}
	p.usageSeen = true
	return p.emit(protocol.CanonicalEvent{Type: protocol.EventUsageUpdated, Usage: protocol.Some(translateUsage(value, false))})
}

func (p *streamParser) emit(event protocol.CanonicalEvent) *protocol.CanonicalError {
	p.sequence++
	event.RequestID = p.request.Canonical().RequestID
	event.AttemptID = p.attempt.ID
	event.RouteID = p.route.ID()
	event.Sequence = p.sequence
	event.ObservedAt = p.adapter.now().UTC()
	if validationFailure := p.validator.Accept(event); validationFailure != nil {
		return validationFailure
	}
	return p.sink(event)
}

func (p *streamParser) invalidJSON(path, rule string) *protocol.CanonicalError {
	return p.withVisibility(protocolFailure(protocol.FailureProtocolInvalidJSON, "The upstream stream contains invalid JSON.", path, rule))
}

func (p *streamParser) invalidSSE(path, rule string) *protocol.CanonicalError {
	return p.withVisibility(protocolFailure(protocol.FailureProtocolInvalidSSE, "The upstream stream is invalid.", path, rule))
}

func (p *streamParser) tooLarge(path, rule string) *protocol.CanonicalError {
	return p.withVisibility(protocolFailure(protocol.FailureUpstreamResponseTooLarge, "The upstream stream exceeded its configured bound.", path, rule))
}

func (p *streamParser) withVisibility(streamFailure *protocol.CanonicalError) *protocol.CanonicalError {
	if streamFailure != nil && p.validator.OutputVisible() {
		streamFailure.OutputVisible = true
		streamFailure.RetryDisposition = protocol.RetryClientDecides
	}
	return streamFailure
}

func sseContentType(value string) bool {
	mediaType, _, _ := strings.Cut(value, ";")
	return strings.EqualFold(strings.TrimSpace(mediaType), "text/event-stream")
}
