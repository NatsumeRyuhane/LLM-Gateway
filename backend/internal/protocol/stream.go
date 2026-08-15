package protocol

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

type EventType string

const (
	EventResponseStarted        EventType = "response.started"
	EventOutputTextDelta        EventType = "output_text.delta"
	EventRefusalDelta           EventType = "refusal.delta"
	EventToolCallStarted        EventType = "tool_call.started"
	EventToolCallArgumentsDelta EventType = "tool_call.arguments.delta"
	EventToolCallCompleted      EventType = "tool_call.completed"
	EventUsageUpdated           EventType = "usage.updated"
	EventResponseCompleted      EventType = "response.completed"
	EventResponseFailed         EventType = "response.failed"
	EventResponseCancelled      EventType = "response.cancelled"
)

type StreamState string

const (
	StreamPrepared  StreamState = "prepared"
	StreamStarted   StreamState = "started"
	StreamActive    StreamState = "active"
	StreamCompleted StreamState = "completed"
	StreamFailed    StreamState = "failed"
	StreamCancelled StreamState = "cancelled"
)

// CanonicalEvent is a tagged union. Only fields belonging to Type may be set.
// Adapters must buffer partial runes and emit complete UTF-8 output-text and
// refusal deltas. Tool-argument fragments may split a code point because their
// exact assembled JSON is validated as UTF-8 when the tool call completes.
type CanonicalEvent struct {
	Type       EventType
	RequestID  string
	AttemptID  string
	RouteID    string
	Sequence   uint64
	ObservedAt time.Time

	Text          string
	ToolCallIndex int
	ToolCallID    string
	ToolName      string
	Usage         Optional[CanonicalUsage]
	FinishReason  FinishReason
	Failure       *CanonicalError
}

type toolCallAssembly struct {
	id        string
	name      string
	arguments strings.Builder
	completed bool
}

// StreamResult is available only after a validated response.completed event.
type StreamResult struct {
	Message      CanonicalMessage
	FinishReason FinishReason
	Usage        Optional[CanonicalUsage]
}

// StreamValidator validates one attempt's events and assembles exact ordered
// output without treating EOF or a parser return as success.
type StreamValidator struct {
	request   ValidatedChatRequest
	attemptID string
	routeID   string
	state     StreamState

	lastSequence uint64
	lastObserved time.Time
	output       strings.Builder
	refusal      strings.Builder
	toolCalls    []*toolCallAssembly
	toolIDs      map[string]struct{}
	usage        Optional[CanonicalUsage]
	finish       FinishReason
	failure      *CanonicalError
	rejected     bool

	outputVisible  bool
	toolActionable bool
}

// NewStreamValidator creates a prepared validator for one route attempt.
func NewStreamValidator(request ValidatedChatRequest, attemptID, routeID string) (*StreamValidator, *CanonicalError) {
	if !request.request.Stream {
		return nil, invalidRequest("stream", "must be true for a canonical event stream")
	}
	if err := validateProtocolIdentifier(attemptID, request.limits.MaxIdentifierBytes, "attempt_id"); err != nil {
		return nil, err
	}
	if err := validateProtocolIdentifier(routeID, request.limits.MaxIdentifierBytes, "route_id"); err != nil {
		return nil, err
	}
	return &StreamValidator{
		request:   request,
		attemptID: attemptID,
		routeID:   routeID,
		state:     StreamPrepared,
		toolIDs:   make(map[string]struct{}),
	}, nil
}

// State reports the current canonical lifecycle state.
func (v *StreamValidator) State() StreamState { return v.state }

// OutputVisible reports whether any model-derived event has crossed the
// canonical visibility boundary within this validator.
func (v *StreamValidator) OutputVisible() bool { return v.outputVisible }

// Accept validates and applies one event atomically.
func (v *StreamValidator) Accept(event CanonicalEvent) *CanonicalError {
	if v == nil {
		return protocolFailure(FailureGatewayInternal, "The gateway could not validate the upstream response.", "stream", "validator is nil", false, false)
	}
	if v.rejected {
		return cloneCanonicalError(v.failure)
	}
	if v.isTerminal() {
		return v.reject(v.eventOrderFailure("event.type", "no event may follow a terminal event"))
	}
	if event.RequestID != v.request.request.RequestID || event.AttemptID != v.attemptID || event.RouteID != v.routeID {
		return v.reject(v.eventOrderFailure("event", "request, attempt, and route identifiers must remain stable"))
	}
	if event.Sequence == 0 || event.Sequence <= v.lastSequence {
		return v.reject(v.eventOrderFailure("event.sequence", "must increase strictly"))
	}
	if event.ObservedAt.IsZero() || (!v.lastObserved.IsZero() && event.ObservedAt.Before(v.lastObserved)) {
		return v.reject(v.eventOrderFailure("event.observed_at", "must be set and monotonic"))
	}

	var err *CanonicalError
	switch event.Type {
	case EventResponseStarted:
		err = v.acceptStarted(event)
	case EventOutputTextDelta:
		err = v.acceptTextDelta(event, false)
	case EventRefusalDelta:
		err = v.acceptTextDelta(event, true)
	case EventToolCallStarted:
		err = v.acceptToolStarted(event)
	case EventToolCallArgumentsDelta:
		err = v.acceptToolArguments(event)
	case EventToolCallCompleted:
		err = v.acceptToolCompleted(event)
	case EventUsageUpdated:
		err = v.acceptUsage(event)
	case EventResponseCompleted:
		err = v.acceptCompleted(event)
	case EventResponseFailed:
		err = v.acceptFailure(event, StreamFailed)
	case EventResponseCancelled:
		err = v.acceptFailure(event, StreamCancelled)
	default:
		err = v.eventOrderFailure("event.type", "is not recognized")
	}
	if err != nil {
		return v.reject(err)
	}
	v.lastSequence = event.Sequence
	v.lastObserved = event.ObservedAt
	return nil
}

// FinalizeEOF validates transport termination. EOF is successful only after a
// response.completed event has already been accepted.
func (v *StreamValidator) FinalizeEOF() *CanonicalError {
	if v == nil {
		return protocolFailure(FailureGatewayInternal, "The gateway could not validate the upstream response.", "stream", "validator is nil", false, false)
	}
	if v.rejected {
		return cloneCanonicalError(v.failure)
	}
	if v.isTerminal() {
		return nil
	}
	return protocolFailure(FailureProtocolEarlyEOF, "The upstream stream ended before canonical completion.", "stream", "requires exactly one terminal event", v.outputVisible, v.toolActionable)
}

// Successful reports a validated response.completed terminal state.
func (v *StreamValidator) Successful() bool {
	return v != nil && !v.rejected && v.state == StreamCompleted
}

// Result returns the canonical assembled result only after successful terminal
// validation. Failed and cancelled streams return their terminal failure.
func (v *StreamValidator) Result() (StreamResult, *CanonicalError) {
	if v == nil {
		return StreamResult{}, protocolFailure(FailureGatewayInternal, "The gateway could not validate the upstream response.", "stream", "validator is nil", false, false)
	}
	if v.state == StreamFailed || v.state == StreamCancelled {
		return StreamResult{}, cloneCanonicalError(v.failure)
	}
	if v.state != StreamCompleted {
		return StreamResult{}, protocolFailure(FailureProtocolEarlyEOF, "The upstream stream ended before canonical completion.", "stream", "result requires response.completed", v.outputVisible, v.toolActionable)
	}
	message := CanonicalMessage{Role: RoleAssistant}
	if v.output.Len() > 0 {
		message.Content = []CanonicalContentPart{{Type: ContentText, Text: v.output.String()}}
	}
	if v.refusal.Len() > 0 {
		message.Refusal = Some(v.refusal.String())
	}
	message.ToolCalls = make([]CanonicalToolCall, len(v.toolCalls))
	for index, call := range v.toolCalls {
		message.ToolCalls[index] = CanonicalToolCall{ID: call.id, Name: call.name, Arguments: call.arguments.String()}
	}
	return StreamResult{Message: message, FinishReason: v.finish, Usage: v.usage}, nil
}

func (v *StreamValidator) acceptStarted(event CanonicalEvent) *CanonicalError {
	if v.state != StreamPrepared {
		return v.eventOrderFailure("event.type", "response.started must occur exactly once and first")
	}
	if !event.emptyPayload() {
		return v.eventOrderFailure("event", "response.started cannot carry output payload")
	}
	v.state = StreamStarted
	return nil
}

func (v *StreamValidator) acceptTextDelta(event CanonicalEvent, refusal bool) *CanonicalError {
	if v.state != StreamStarted && v.state != StreamActive {
		return v.eventOrderFailure("event.type", "text and refusal deltas require response.started")
	}
	if event.Text == "" || !utf8.ValidString(event.Text) || !event.onlyTextPayload() {
		return v.eventOrderFailure("event.text", "must be a non-empty UTF-8 delta with no unrelated payload")
	}
	current := v.output.Len() + v.refusal.Len()
	if len(event.Text) > v.request.limits.MaxEventTextBytes || current+len(event.Text) > v.request.limits.MaxResponseTextBytes {
		return v.streamFailure(FailureUpstreamResponseTooLarge, "event.text", "exceeds the configured text bound")
	}
	if refusal {
		v.refusal.WriteString(event.Text)
	} else {
		v.output.WriteString(event.Text)
	}
	v.state = StreamActive
	v.outputVisible = true
	return nil
}

func (v *StreamValidator) acceptToolStarted(event CanonicalEvent) *CanonicalError {
	if v.state != StreamStarted && v.state != StreamActive {
		return v.eventOrderFailure("event.type", "tool_call.started requires response.started")
	}
	if !event.onlyToolStartPayload() || event.ToolCallIndex != len(v.toolCalls) {
		return v.eventOrderFailure("event.tool_call_index", "tool calls must start once in increasing zero-based order")
	}
	if len(v.toolCalls) >= v.request.limits.MaxToolCallsPerMessage {
		return v.streamFailure(FailureUpstreamResponseTooLarge, "event.tool_call_index", "exceeds the tool-call limit")
	}
	if parallel, explicit := v.request.request.ParallelToolCalls.Get(); explicit && !parallel && len(v.toolCalls) > 0 {
		return v.streamFailure(FailureProtocolInvalidToolCall, "event.tool_call_index", "multiple tool calls violate explicit parallel_tool_calls false")
	}
	if err := validateProtocolIdentifier(event.ToolCallID, v.request.limits.MaxIdentifierBytes, "event.tool_call_id"); err != nil {
		return v.withVisibility(err)
	}
	if _, duplicate := v.toolIDs[event.ToolCallID]; duplicate {
		return v.streamFailure(FailureProtocolInvalidToolCall, "event.tool_call_id", "must be unique")
	}
	if !validCanonicalName(event.ToolName, v.request.limits.MaxToolNameBytes) {
		return v.streamFailure(FailureProtocolInvalidToolCall, "event.tool_name", "is invalid")
	}
	if _, declared := v.request.toolSchemas[event.ToolName]; !declared {
		return v.streamFailure(FailureProtocolInvalidToolCall, "event.tool_name", "must name a declared function")
	}
	v.toolIDs[event.ToolCallID] = struct{}{}
	v.toolCalls = append(v.toolCalls, &toolCallAssembly{id: event.ToolCallID, name: event.ToolName})
	v.state = StreamActive
	v.outputVisible = true
	return nil
}

func (v *StreamValidator) acceptToolArguments(event CanonicalEvent) *CanonicalError {
	if v.state != StreamActive || !event.onlyToolArgumentsPayload() {
		return v.eventOrderFailure("event.type", "tool argument deltas require an active started tool call")
	}
	call, err := v.openToolCall(event.ToolCallIndex)
	if err != nil {
		return err
	}
	if event.Text == "" {
		return v.streamFailure(FailureProtocolInvalidToolCall, "event.text", "tool argument fragments must be non-empty")
	}
	if len(event.Text) > v.request.limits.MaxEventTextBytes || call.arguments.Len()+len(event.Text) > v.request.limits.MaxToolArgumentsBytes {
		return v.streamFailure(FailureUpstreamResponseTooLarge, "event.text", "exceeds the tool-argument bound")
	}
	call.arguments.WriteString(event.Text)
	v.outputVisible = true
	return nil
}

func (v *StreamValidator) acceptToolCompleted(event CanonicalEvent) *CanonicalError {
	if v.state != StreamActive || !event.onlyToolCompletedPayload() {
		return v.eventOrderFailure("event.type", "tool_call.completed requires an active started tool call")
	}
	call, err := v.openToolCall(event.ToolCallIndex)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("tool_calls[%d].arguments", event.ToolCallIndex)
	arguments, violation := decodeBoundedJSON([]byte(call.arguments.String()), v.request.limits.MaxToolArgumentsBytes, v.request.limits.MaxSchemaDepth, path)
	if violation != nil {
		return v.streamFailure(FailureProtocolInvalidToolCall, violation.path, violation.rule)
	}
	if _, ok := arguments.(map[string]any); !ok {
		return v.streamFailure(FailureProtocolInvalidToolCall, path, "must be a JSON object")
	}
	if violation := validateJSONAgainstSchema(arguments, v.request.toolSchemas[call.name], path); violation != nil {
		return v.streamFailure(FailureProtocolInvalidToolCall, violation.path, violation.rule)
	}
	call.completed = true
	v.outputVisible = true
	v.toolActionable = true
	return nil
}

func (v *StreamValidator) acceptUsage(event CanonicalEvent) *CanonicalError {
	if v.state != StreamStarted && v.state != StreamActive {
		return v.eventOrderFailure("event.type", "usage.updated requires response.started")
	}
	usage, present := event.Usage.Get()
	if !present || !event.onlyUsagePayload() {
		return v.eventOrderFailure("event.usage", "usage.updated must carry only usage")
	}
	if previous, exists := v.usage.Get(); exists {
		if violation := validateUsageMonotonic(previous, usage, "event.usage"); violation != nil {
			return v.streamFailure(FailureProtocolUsageInconsistent, violation.path, violation.rule)
		}
	} else if violation := validateUsageValue(usage, "event.usage"); violation != nil {
		return v.streamFailure(FailureProtocolUsageInconsistent, violation.path, violation.rule)
	}
	v.usage = Some(usage)
	return nil
}

func (v *StreamValidator) acceptCompleted(event CanonicalEvent) *CanonicalError {
	if v.state != StreamActive || !event.onlyCompletedPayload() {
		return v.eventOrderFailure("event.type", "response.completed requires active output and only a finish reason")
	}
	if err := validateFinishReason(event.FinishReason, "event.finish_reason", v.outputVisible, v.toolActionable); err != nil {
		return err
	}
	for index, call := range v.toolCalls {
		if !call.completed {
			return v.streamFailure(FailureProtocolInvalidToolCall, fmt.Sprintf("tool_calls[%d]", index), "must complete before response.completed")
		}
	}
	if len(v.toolCalls) > 0 && event.FinishReason != FinishToolCalls {
		return v.eventOrderFailure("event.finish_reason", "must be tool_calls when tool calls are present")
	}
	if len(v.toolCalls) == 0 && event.FinishReason == FinishToolCalls {
		return v.eventOrderFailure("event.finish_reason", "requires at least one tool call")
	}
	if v.output.Len() == 0 && v.refusal.Len() == 0 && len(v.toolCalls) == 0 {
		return v.streamFailure(FailureProtocolEmptyOutput, "stream", "cannot complete without model output")
	}
	if usage, present := v.usage.Get(); present && usage.Partial {
		return v.streamFailure(FailureProtocolUsageInconsistent, "event.usage.partial", "must be false before response.completed")
	}
	if v.output.Len() > 0 && len(v.toolCalls) == 0 {
		if err := validateStructuredText(v.output.String(), v.request, v.outputVisible, v.toolActionable); err != nil {
			return err
		}
	}
	v.finish = event.FinishReason
	v.state = StreamCompleted
	return nil
}

func (v *StreamValidator) acceptFailure(event CanonicalEvent, terminal StreamState) *CanonicalError {
	if (v.state != StreamStarted && v.state != StreamActive) || !event.onlyFailurePayload() {
		return v.eventOrderFailure("event.type", "terminal failure requires response.started and only a canonical failure")
	}
	if violation := validateFailureEnvelope(event.Failure, v.request.limits); violation != nil {
		return v.eventOrderFailure(violation.path, violation.rule)
	}
	if event.Failure.RequestID != event.RequestID {
		return v.eventOrderFailure("event.failure.request_id", "must match the stream request")
	}
	if event.Failure.AttemptID != "" && event.Failure.AttemptID != event.AttemptID {
		return v.eventOrderFailure("event.failure.attempt_id", "must match the stream attempt when present")
	}
	if event.Failure.RouteID != "" && event.Failure.RouteID != event.RouteID {
		return v.eventOrderFailure("event.failure.route_id", "must match the stream route when present")
	}
	if v.outputVisible {
		if !event.Failure.OutputVisible {
			return v.eventOrderFailure("event.failure.output_visible", "must record prior visible output")
		}
		if event.Failure.RetryDisposition == RetryPreOutputAlternate || event.Failure.RetryDisposition == RetryPreOutputSameOrAlternate {
			return v.eventOrderFailure("event.failure.retry_disposition", "cannot permit automatic retry after visible output")
		}
	}
	if v.toolActionable && !event.Failure.ToolActionable {
		return v.eventOrderFailure("event.failure.tool_actionable", "must record the completed tool action boundary")
	}
	v.failure = cloneCanonicalError(event.Failure)
	v.state = terminal
	return nil
}

func (v *StreamValidator) openToolCall(index int) (*toolCallAssembly, *CanonicalError) {
	if index < 0 || index >= len(v.toolCalls) {
		return nil, v.streamFailure(FailureProtocolInvalidToolCall, "event.tool_call_index", "does not reference a started tool call")
	}
	call := v.toolCalls[index]
	if call.completed {
		return nil, v.streamFailure(FailureProtocolInvalidToolCall, "event.tool_call_index", "tool call is already complete")
	}
	return call, nil
}

func (v *StreamValidator) isTerminal() bool {
	return v.state == StreamCompleted || v.state == StreamFailed || v.state == StreamCancelled
}

func (v *StreamValidator) eventOrderFailure(path, rule string) *CanonicalError {
	return protocolFailure(FailureProtocolInvalidEventOrder, "The upstream stream has invalid event order.", path, rule, v.outputVisible, v.toolActionable)
}

func (v *StreamValidator) streamFailure(code FailureCode, path, rule string) *CanonicalError {
	message := "The upstream stream is invalid."
	switch code {
	case FailureProtocolInvalidToolCall:
		message = "The upstream tool call is invalid."
	case FailureProtocolUsageInconsistent:
		message = "The upstream usage is inconsistent."
	}
	return protocolFailure(code, message, path, rule, v.outputVisible, v.toolActionable)
}

func (v *StreamValidator) reject(err *CanonicalError) *CanonicalError {
	if v.rejected {
		return cloneCanonicalError(v.failure)
	}
	v.failure = cloneCanonicalError(err)
	v.state = StreamFailed
	v.rejected = true
	return err
}

func (v *StreamValidator) withVisibility(err *CanonicalError) *CanonicalError {
	if err == nil {
		return nil
	}
	err.OutputVisible = v.outputVisible
	err.ToolActionable = v.toolActionable
	if v.outputVisible || v.toolActionable {
		err.RetryDisposition = RetryClientDecides
	}
	return err
}

func validateFailureEnvelope(failure *CanonicalError, limits Limits) *schemaViolation {
	if failure == nil {
		return &schemaViolation{path: "event.failure", rule: "is required"}
	}
	if !knownFailureCode(failure.Code) {
		return &schemaViolation{path: "event.failure.code", rule: "is not recognized"}
	}
	if !knownFailureDomain(failure.Domain) || !failureCodeMatchesDomain(failure.Code, failure.Domain) {
		return &schemaViolation{path: "event.failure.domain", rule: "does not match the stable failure code"}
	}
	switch failure.RetryDisposition {
	case RetryNever, RetryPreOutputAlternate, RetryPreOutputSameOrAlternate, RetryClientDecides:
	default:
		return &schemaViolation{path: "event.failure.retry_disposition", rule: "is not recognized"}
	}
	if failure.SafeMessage == "" || !utf8.ValidString(failure.SafeMessage) || len(failure.SafeMessage) > limits.MaxSafeErrorBytes || containsControl(failure.SafeMessage) {
		return &schemaViolation{path: "event.failure.safe_message", rule: "must be bounded non-empty UTF-8"}
	}
	if failure.HTTPStatus < 100 || failure.HTTPStatus > 599 {
		return &schemaViolation{path: "event.failure.http_status", rule: "must be a valid HTTP status"}
	}
	return nil
}

func containsControl(value string) bool {
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return true
		}
	}
	return false
}

func knownFailureCode(code FailureCode) bool {
	switch code {
	case FailureClientInvalidRequest, FailureClientCancelled, FailureClientDeadlineExceeded,
		FailureAuthMissingCredential, FailureAuthInvalidCredential, FailureAuthForbidden, FailureQuotaGatewayExceeded,
		FailurePolicyUnknownTarget, FailurePolicyNoEligibleRoute, FailurePolicyAllRoutesOpen, FailureCapabilityUnsupported, FailureAffinityRouteIneligible,
		FailureGatewayOverloaded, FailureGatewayInternal, FailureGatewayDownstreamWriteFailed, FailureGatewayShutdown, FailureStorageUnavailable, FailureTelemetryExportFailed,
		FailureUpstreamDNSFailed, FailureUpstreamConnectFailed, FailureUpstreamTLSFailed, FailureUpstreamTimeout, FailureUpstreamStreamStalled,
		FailureUpstreamRedirectRejected, FailureUpstreamResponseTooLarge, FailureUpstreamAuthenticationFailed, FailureUpstreamPermissionDenied,
		FailureUpstreamRateLimited, FailureUpstreamServerError, FailureUpstreamContentPolicy, FailureUpstreamContextLimit, FailureUpstreamInvalidStatus,
		FailureProtocolInvalidJSON, FailureProtocolInvalidSSE, FailureProtocolEarlyEOF, FailureProtocolEmptyOutput, FailureProtocolInvalidEventOrder,
		FailureProtocolInvalidToolCall, FailureProtocolInvalidStructured, FailureProtocolUsageInconsistent, FailureProtocolParameterIgnored:
		return true
	default:
		return false
	}
}

func knownFailureDomain(domain FailureDomain) bool {
	switch domain {
	case DomainClient, DomainAuth, DomainQuota, DomainPolicy, DomainCapability, DomainAffinity, DomainGateway, DomainStorage, DomainTelemetry, DomainUpstream, DomainProtocol:
		return true
	default:
		return false
	}
}

func failureCodeMatchesDomain(code FailureCode, domain FailureDomain) bool {
	expected, _ := failureEnvelopeMetadata(code)
	return expected == domain
}

func cloneCanonicalError(source *CanonicalError) *CanonicalError {
	if source == nil {
		return nil
	}
	cloned := *source
	if source.Validation != nil {
		issue := *source.Validation
		cloned.Validation = &issue
	}
	return &cloned
}

func (e CanonicalEvent) emptyPayload() bool {
	return e.Text == "" && e.ToolCallIndex == 0 && e.ToolCallID == "" && e.ToolName == "" && !e.Usage.IsSet() && e.FinishReason == "" && e.Failure == nil
}

func (e CanonicalEvent) onlyTextPayload() bool {
	return e.ToolCallIndex == 0 && e.ToolCallID == "" && e.ToolName == "" && !e.Usage.IsSet() && e.FinishReason == "" && e.Failure == nil
}

func (e CanonicalEvent) onlyToolStartPayload() bool {
	return e.Text == "" && e.ToolCallID != "" && e.ToolName != "" && !e.Usage.IsSet() && e.FinishReason == "" && e.Failure == nil
}

func (e CanonicalEvent) onlyToolArgumentsPayload() bool {
	return e.ToolCallID == "" && e.ToolName == "" && !e.Usage.IsSet() && e.FinishReason == "" && e.Failure == nil
}

func (e CanonicalEvent) onlyToolCompletedPayload() bool {
	return e.Text == "" && e.ToolCallID == "" && e.ToolName == "" && !e.Usage.IsSet() && e.FinishReason == "" && e.Failure == nil
}

func (e CanonicalEvent) onlyUsagePayload() bool {
	return e.Text == "" && e.ToolCallIndex == 0 && e.ToolCallID == "" && e.ToolName == "" && e.FinishReason == "" && e.Failure == nil
}

func (e CanonicalEvent) onlyCompletedPayload() bool {
	return e.Text == "" && e.ToolCallIndex == 0 && e.ToolCallID == "" && e.ToolName == "" && !e.Usage.IsSet() && e.FinishReason != "" && e.Failure == nil
}

func (e CanonicalEvent) onlyFailurePayload() bool {
	return e.Text == "" && e.ToolCallIndex == 0 && e.ToolCallID == "" && e.ToolName == "" && !e.Usage.IsSet() && e.FinishReason == "" && e.Failure != nil
}
