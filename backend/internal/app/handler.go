package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/openai"
	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/protocol"
	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/provider"
	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/telemetry"
)

const defaultDataPlaneTimeout = 2 * time.Minute

// EvidenceSink is the narrow metadata-only recording boundary used by the
// vertical slice. Implementations must not block response cleanup indefinitely.
type EvidenceSink interface {
	RecordRequest(telemetry.RequestEvidence)
	RecordDecision(telemetry.DecisionEvidence)
	RecordAttempt(telemetry.AttemptEvidence)
}

// IdentifierGenerator creates bounded opaque correlation identifiers.
type IdentifierGenerator func(prefix string) (string, error)

// DataPlaneHandlerOptions controls lifecycle-owned values and test seams.
type DataPlaneHandlerOptions struct {
	Now            func() time.Time
	RequestTimeout time.Duration
	NewIdentifier  IdentifierGenerator
}

// DefaultDataPlaneHandlerOptions returns production lifecycle defaults.
func DefaultDataPlaneHandlerOptions() DataPlaneHandlerOptions {
	return DataPlaneHandlerOptions{
		Now: time.Now, RequestTimeout: defaultDataPlaneTimeout, NewIdentifier: randomIdentifier,
	}
}

// DataPlaneHandler composes authentication, one authorized route, one adapter
// attempt, public encoding, cancellation, and metadata-only terminal evidence.
type DataPlaneHandler struct {
	boundary *DataPlaneBoundary
	route    *SingleAuthorizedRoute
	adapter  provider.Adapter
	evidence EvidenceSink
	now      func() time.Time
	timeout  time.Duration
	newID    IdentifierGenerator
	codec    openai.Codec
}

// NewDataPlaneHandler creates the complete authenticated v0 data-plane slice.
func NewDataPlaneHandler(
	boundary *DataPlaneBoundary,
	route *SingleAuthorizedRoute,
	adapter provider.Adapter,
	evidence EvidenceSink,
	options DataPlaneHandlerOptions,
) (*DataPlaneHandler, error) {
	if boundary == nil || route == nil || adapter == nil {
		return nil, errors.New("data-plane boundary, route, and adapter are required")
	}
	defaults := DefaultDataPlaneHandlerOptions()
	if options.Now == nil {
		options.Now = defaults.Now
	}
	if options.RequestTimeout == 0 {
		options.RequestTimeout = defaults.RequestTimeout
	}
	if options.NewIdentifier == nil {
		options.NewIdentifier = defaults.NewIdentifier
	}
	if options.RequestTimeout < 0 {
		return nil, errors.New("data-plane request timeout must be positive")
	}
	if evidence == nil {
		evidence = discardEvidence{}
	}
	return &DataPlaneHandler{
		boundary: boundary, route: route, adapter: adapter, evidence: evidence,
		now: options.Now, timeout: options.RequestTimeout, newID: options.NewIdentifier, codec: boundary.codec,
	}, nil
}

func (h *DataPlaneHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request == nil || request.URL == nil {
		http.NotFound(writer, request)
		return
	}
	switch request.URL.Path {
	case openai.ModelsPath:
		h.serveModels(writer, request)
	case openai.ChatCompletionsPath:
		h.serveChat(writer, request)
	default:
		http.NotFound(writer, request)
	}
}

func (h *DataPlaneHandler) serveModels(writer http.ResponseWriter, request *http.Request) {
	started := h.now().UTC()
	record := telemetry.RequestEvidence{
		SchemaVersion: telemetry.RequestEvidenceSchema, OccurredAt: started,
		TrafficClass: telemetry.TrafficOrdinary, Operation: telemetry.OperationModelsList,
	}
	defer func() {
		record.TotalLatency = nonnegativeDuration(started, h.now().UTC())
		h.evidence.RecordRequest(record)
	}()

	requestID, failure := h.identifier("req")
	record.RequestID = requestID
	if failure != nil {
		h.finishFailure(writer, requestID, failure, &record)
		return
	}
	metadata := openai.RequestMetadata{RequestID: requestID, Deadline: h.deadline(request)}
	decoded, authFailure := h.boundary.AuthenticateModelsRequest(request, metadata)
	if authFailure != nil {
		setAllowHeader(writer, authFailure, http.MethodGet)
		h.finishFailure(writer, requestID, authFailure, &record)
		return
	}
	record.Admitted = true
	encoded, encodeFailure := h.codec.EncodeModelsResponse(requestID, h.route.models(request.Context(), decoded.Principal))
	if encodeFailure != nil {
		h.finishFailure(writer, requestID, encodeFailure, &record)
		return
	}
	if writeErr := writeEncoded(writer, encoded); writeErr != nil {
		record.Outcome = telemetry.OutcomeIncomplete
		return
	}
	record.Outcome = telemetry.OutcomeSuccess
}

func (h *DataPlaneHandler) serveChat(writer http.ResponseWriter, request *http.Request) {
	started := h.now().UTC()
	requestRecord := telemetry.RequestEvidence{
		SchemaVersion: telemetry.RequestEvidenceSchema, OccurredAt: started,
		TrafficClass: telemetry.TrafficOrdinary, Operation: telemetry.OperationChatCompletions,
	}
	defer func() {
		requestRecord.TotalLatency = nonnegativeDuration(started, h.now().UTC())
		h.evidence.RecordRequest(requestRecord)
	}()

	requestID, failure := h.identifier("req")
	requestRecord.RequestID = requestID
	if failure != nil {
		h.finishFailure(writer, requestID, failure, &requestRecord)
		return
	}
	metadata := openai.RequestMetadata{RequestID: requestID, Deadline: h.deadline(request)}
	decoded, authFailure := h.boundary.AuthenticateChatCompletionsRequest(request, metadata)
	if authFailure != nil {
		setAllowHeader(writer, authFailure, http.MethodPost)
		h.finishFailure(writer, requestID, authFailure, &requestRecord)
		return
	}
	canonical := decoded.Request.Canonical()
	requestRecord.Stream = canonical.Stream
	requestRecord.RequestedTarget = canonical.Target

	decisionID, idFailure := h.identifier("dec")
	if idFailure != nil {
		h.finishFailure(writer, requestID, idFailure, &requestRecord)
		return
	}
	requestRecord.DecisionID = decisionID
	decision := telemetry.DecisionEvidence{
		SchemaVersion: telemetry.DecisionEvidenceSchema, DecisionID: decisionID, RequestID: requestID,
		OccurredAt: h.now().UTC(), TrafficClass: telemetry.TrafficOrdinary,
		ApplicationID: decoded.Principal.ApplicationID, RequestedTarget: canonical.Target,
	}
	defer func() { h.evidence.RecordDecision(decision) }()

	route, routeFailure := h.route.resolve(request.Context(), decoded.Principal, decoded.Request)
	if routeFailure != nil {
		decision.Outcome = outcomeForFailure(routeFailure, false)
		decision.FailureCode = routeFailure.Code
		h.finishFailure(writer, requestID, routeFailure, &requestRecord)
		return
	}
	decision.SelectedRouteID = route.ID()

	responseID := ""
	if canonical.Stream {
		responseID, idFailure = h.identifier("chatcmpl")
		if idFailure != nil {
			decision.Outcome = telemetry.OutcomeFailed
			decision.FailureCode = idFailure.Code
			h.finishFailure(writer, requestID, idFailure, &requestRecord)
			return
		}
	}
	attemptID, idFailure := h.identifier("att")
	if idFailure != nil {
		decision.Outcome = telemetry.OutcomeFailed
		decision.FailureCode = idFailure.Code
		h.finishFailure(writer, requestID, idFailure, &requestRecord)
		return
	}
	requestRecord.Admitted = true
	decision.AttemptID = attemptID
	attemptStarted := h.now().UTC()
	requestRecord.DispatchLatency = nonnegativeDuration(started, attemptStarted)
	attemptRecord := telemetry.AttemptEvidence{
		SchemaVersion: telemetry.AttemptEvidenceSchema, AttemptID: attemptID, DecisionID: decisionID,
		RequestID: requestID, Ordinal: 1, RouteID: route.ID(), StartedAt: attemptStarted,
	}
	defer func() {
		attemptRecord.EndedAt = h.now().UTC()
		attemptRecord.TotalLatency = nonnegativeDuration(attemptStarted, attemptRecord.EndedAt)
		if !attemptRecord.CanonicalAcceptedAt.IsZero() {
			attemptRecord.TimeToCanonicalOutput = nonnegativeDuration(attemptStarted, attemptRecord.CanonicalAcceptedAt)
		}
		if !attemptRecord.DownstreamCommittedAt.IsZero() {
			attemptRecord.TimeToDownstreamCommit = nonnegativeDuration(attemptStarted, attemptRecord.DownstreamCommittedAt)
		}
		h.evidence.RecordAttempt(attemptRecord)
	}()

	if canonical.Stream {
		h.serveStreamingAttempt(writer, request, decoded.Request, route, provider.Attempt{ID: attemptID}, responseID, &requestRecord, &decision, &attemptRecord)
		return
	}
	h.serveBufferedAttempt(writer, request, decoded.Request, route, provider.Attempt{ID: attemptID}, &requestRecord, &decision, &attemptRecord)
}

func (h *DataPlaneHandler) serveBufferedAttempt(
	writer http.ResponseWriter,
	request *http.Request,
	validated protocol.ValidatedChatRequest,
	route provider.ValidatedRoute,
	attempt provider.Attempt,
	requestRecord *telemetry.RequestEvidence,
	decision *telemetry.DecisionEvidence,
	attemptRecord *telemetry.AttemptEvidence,
) {
	response, failure := h.adapter.Buffered(request.Context(), attempt, validated, route)
	if failure != nil {
		h.finishAttemptFailure(writer, failure, requestRecord, decision, attemptRecord)
		return
	}
	acceptedAt := h.now().UTC()
	attemptRecord.CanonicalOutputAccepted = true
	attemptRecord.CanonicalAcceptedAt = acceptedAt
	canonical := response.Canonical()
	attemptRecord.Usage = usageEvidence(canonical.Usage)
	requestRecord.Usage = attemptRecord.Usage
	encoded, encodeFailure := h.codec.EncodeBufferedChatCompletion(response, correlationVisibility())
	if encodeFailure != nil {
		h.finishAttemptFailure(writer, attachCorrelation(encodeFailure, validated.Canonical().RequestID, attempt.ID, route.ID()), requestRecord, decision, attemptRecord)
		return
	}
	attemptRecord.DownstreamCommitted = true
	attemptRecord.DownstreamCommittedAt = h.now().UTC()
	if writeErr := writeEncoded(writer, encoded); writeErr != nil {
		failure = downstreamWriteFailure(validated.Canonical().RequestID, attempt.ID, route.ID(), true, false)
		h.finishAttemptWithoutEnvelope(failure, requestRecord, decision, attemptRecord)
		return
	}
	attemptRecord.Terminal = telemetry.AttemptCompleted
	requestRecord.Outcome = telemetry.OutcomeSuccess
	decision.Outcome = telemetry.OutcomeSuccess
}

func (h *DataPlaneHandler) serveStreamingAttempt(
	writer http.ResponseWriter,
	request *http.Request,
	validated protocol.ValidatedChatRequest,
	route provider.ValidatedRoute,
	attempt provider.Attempt,
	responseID string,
	requestRecord *telemetry.RequestEvidence,
	decision *telemetry.DecisionEvidence,
	attemptRecord *telemetry.AttemptEvidence,
) {
	encoder, failure := h.codec.NewStreamEncoder(validated, attempt.ID, route.ID(), openai.StreamMetadata{
		ResponseID: responseID, CreatedAt: h.now().UTC(),
	}, correlationVisibility())
	if failure != nil {
		h.finishAttemptFailure(writer, attachCorrelation(failure, validated.Canonical().RequestID, attempt.ID, route.ID()), requestRecord, decision, attemptRecord)
		return
	}
	attemptCtx, cancel := context.WithCancel(request.Context())
	defer cancel()
	controller := http.NewResponseController(writer)
	sink := func(event protocol.CanonicalEvent) *protocol.CanonicalError {
		wasAccepted := encoder.OutputVisible()
		frames, encodeFailure := encoder.Encode(event)
		if encodeFailure != nil {
			cancel()
			return attachCorrelation(encodeFailure, validated.Canonical().RequestID, attempt.ID, route.ID())
		}
		if !wasAccepted && encoder.OutputVisible() {
			attemptRecord.CanonicalOutputAccepted = true
			attemptRecord.CanonicalAcceptedAt = h.now().UTC()
		}
		if event.Type == protocol.EventToolCallCompleted {
			attemptRecord.ToolActionable = true
		}
		if usage, present := event.Usage.Get(); present {
			evidence := usageEvidence(protocol.Some(usage))
			attemptRecord.Usage = evidence
			requestRecord.Usage = evidence
		}
		if len(frames) == 0 {
			return nil
		}
		if !attemptRecord.DownstreamCommitted {
			copyHeaders(writer.Header(), encoder.Header())
			attemptRecord.DownstreamCommitted = true
			attemptRecord.DownstreamCommittedAt = h.now().UTC()
			writer.WriteHeader(http.StatusOK)
		}
		if _, err := writeAll(writer, frames); err != nil {
			cancel()
			return downstreamWriteFailure(validated.Canonical().RequestID, attempt.ID, route.ID(), true, attemptRecord.ToolActionable)
		}
		if err := controller.Flush(); err != nil {
			cancel()
			return downstreamWriteFailure(validated.Canonical().RequestID, attempt.ID, route.ID(), true, attemptRecord.ToolActionable)
		}
		return nil
	}
	result, streamFailure := h.adapter.Stream(attemptCtx, attempt, validated, route, sink)
	if streamFailure != nil {
		if attemptRecord.DownstreamCommitted {
			h.finishAttemptWithoutEnvelope(streamFailure, requestRecord, decision, attemptRecord)
			return
		}
		h.finishAttemptFailure(writer, streamFailure, requestRecord, decision, attemptRecord)
		return
	}
	if !encoder.Successful() || !attemptRecord.DownstreamCommitted {
		failure = streamInvariantFailure(validated.Canonical().RequestID, attempt.ID, route.ID(), attemptRecord.DownstreamCommitted, attemptRecord.ToolActionable)
		if attemptRecord.DownstreamCommitted {
			h.finishAttemptWithoutEnvelope(failure, requestRecord, decision, attemptRecord)
			return
		}
		h.finishAttemptFailure(writer, failure, requestRecord, decision, attemptRecord)
		return
	}
	attemptRecord.Usage = usageEvidence(result.Usage)
	requestRecord.Usage = attemptRecord.Usage
	attemptRecord.Terminal = telemetry.AttemptCompleted
	requestRecord.Outcome = telemetry.OutcomeSuccess
	decision.Outcome = telemetry.OutcomeSuccess
}

func (h *DataPlaneHandler) finishAttemptFailure(
	writer http.ResponseWriter,
	failure *protocol.CanonicalError,
	requestRecord *telemetry.RequestEvidence,
	decision *telemetry.DecisionEvidence,
	attemptRecord *telemetry.AttemptEvidence,
) {
	h.finishAttemptWithoutEnvelope(failure, requestRecord, decision, attemptRecord)
	encoded := h.codec.EncodeError(failure, correlationVisibility())
	_ = writeEncoded(writer, encoded)
}

func (h *DataPlaneHandler) finishAttemptWithoutEnvelope(
	failure *protocol.CanonicalError,
	requestRecord *telemetry.RequestEvidence,
	decision *telemetry.DecisionEvidence,
	attemptRecord *telemetry.AttemptEvidence,
) {
	attemptRecord.Terminal = terminalForFailure(failure, attemptRecord.DownstreamCommitted)
	attemptRecord.FailureCode = failure.Code
	attemptRecord.RetryDisposition = failure.RetryDisposition
	attemptRecord.ProviderStatus = failure.ProviderStatus
	attemptRecord.ToolActionable = attemptRecord.ToolActionable || failure.ToolActionable
	requestRecord.Outcome = outcomeForFailure(failure, attemptRecord.DownstreamCommitted)
	requestRecord.FailureDomain = failure.Domain
	requestRecord.FailureCode = failure.Code
	decision.Outcome = requestRecord.Outcome
	decision.FailureCode = failure.Code
}

func (h *DataPlaneHandler) finishFailure(writer http.ResponseWriter, requestID string, failure *protocol.CanonicalError, record *telemetry.RequestEvidence) {
	failure = attachCorrelation(failure, requestID, "", "")
	record.Outcome = outcomeForFailure(failure, false)
	record.FailureDomain = failure.Domain
	record.FailureCode = failure.Code
	_ = writeEncoded(writer, h.codec.EncodeError(failure, openai.CorrelationVisibility{}))
}

func (h *DataPlaneHandler) deadline(request *http.Request) time.Time {
	now := h.now().UTC()
	deadline := now.Add(h.timeout)
	if request != nil {
		if existing, present := request.Context().Deadline(); present && existing.Before(deadline) {
			return existing.UTC()
		}
	}
	return deadline
}

func (h *DataPlaneHandler) identifier(prefix string) (string, *protocol.CanonicalError) {
	value, err := h.newID(prefix)
	if err != nil || !boundedIdentifier(value, protocol.DefaultLimits().MaxIdentifierBytes) {
		return "", &protocol.CanonicalError{
			Code: protocol.FailureGatewayInternal, Domain: protocol.DomainGateway,
			RetryDisposition: protocol.RetryNever, SafeMessage: "The gateway could not create request correlation.",
			HTTPStatus: http.StatusInternalServerError,
		}
	}
	return value, nil
}

func randomIdentifier(prefix string) (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(value[:]), nil
}

func correlationVisibility() openai.CorrelationVisibility {
	return openai.CorrelationVisibility{AttemptID: true, RouteID: true}
}

func writeEncoded(writer http.ResponseWriter, response openai.EncodedResponse) error {
	copyHeaders(writer.Header(), response.Header)
	writer.WriteHeader(response.Status)
	_, err := writeAll(writer, response.Body)
	return err
}

func writeAll(writer io.Writer, body []byte) (int, error) {
	written := 0
	for written < len(body) {
		count, err := writer.Write(body[written:])
		written += count
		if err != nil {
			return written, err
		}
		if count == 0 {
			return written, io.ErrShortWrite
		}
	}
	return written, nil
}

func copyHeaders(destination, source http.Header) {
	for name, values := range source {
		destination[name] = append([]string(nil), values...)
	}
}

func attachCorrelation(failure *protocol.CanonicalError, requestID, attemptID, routeID string) *protocol.CanonicalError {
	if failure == nil {
		failure = &protocol.CanonicalError{
			Code: protocol.FailureGatewayInternal, Domain: protocol.DomainGateway,
			RetryDisposition: protocol.RetryNever, SafeMessage: "The gateway could not complete the request.",
			HTTPStatus: http.StatusInternalServerError,
		}
	}
	attached := *failure
	if failure.Validation != nil {
		validation := *failure.Validation
		attached.Validation = &validation
	}
	attached.RequestID = requestID
	if attemptID != "" {
		attached.AttemptID = attemptID
	}
	if routeID != "" {
		attached.RouteID = routeID
	}
	return &attached
}

func downstreamWriteFailure(requestID, attemptID, routeID string, outputVisible, toolActionable bool) *protocol.CanonicalError {
	retry := protocol.RetryNever
	if outputVisible || toolActionable {
		retry = protocol.RetryClientDecides
	}
	return &protocol.CanonicalError{
		Code: protocol.FailureGatewayDownstreamWriteFailed, Domain: protocol.DomainGateway,
		RetryDisposition: retry, SafeMessage: "The gateway could not write the downstream response.", HTTPStatus: http.StatusInternalServerError,
		RequestID: requestID, AttemptID: attemptID, RouteID: routeID,
		OutputVisible: outputVisible, ToolActionable: toolActionable,
	}
}

func streamInvariantFailure(requestID, attemptID, routeID string, outputVisible, toolActionable bool) *protocol.CanonicalError {
	retry := protocol.RetryNever
	if outputVisible || toolActionable {
		retry = protocol.RetryClientDecides
	}
	return &protocol.CanonicalError{
		Code: protocol.FailureGatewayInternal, Domain: protocol.DomainGateway,
		RetryDisposition: retry, SafeMessage: "The gateway could not complete the streaming response.", HTTPStatus: http.StatusInternalServerError,
		RequestID: requestID, AttemptID: attemptID, RouteID: routeID,
		OutputVisible: outputVisible, ToolActionable: toolActionable,
	}
}

func setAllowHeader(writer http.ResponseWriter, failure *protocol.CanonicalError, method string) {
	if failure != nil && failure.HTTPStatus == http.StatusMethodNotAllowed {
		writer.Header().Set("Allow", method)
	}
}

func outcomeForFailure(failure *protocol.CanonicalError, downstreamCommitted bool) telemetry.Outcome {
	if downstreamCommitted || failure.OutputVisible || failure.ToolActionable {
		return telemetry.OutcomePartial
	}
	if failure.Code == protocol.FailureClientCancelled || failure.Code == protocol.FailureClientDeadlineExceeded {
		return telemetry.OutcomeCancelled
	}
	switch failure.Domain {
	case protocol.DomainAuth, protocol.DomainClient, protocol.DomainCapability, protocol.DomainPolicy, protocol.DomainAffinity, protocol.DomainQuota:
		return telemetry.OutcomeRejected
	}
	return telemetry.OutcomeFailed
}

func terminalForFailure(failure *protocol.CanonicalError, downstreamCommitted bool) telemetry.AttemptTerminal {
	switch failure.Code {
	case protocol.FailureClientCancelled:
		return telemetry.AttemptCancelledClient
	case protocol.FailureClientDeadlineExceeded:
		return telemetry.AttemptCancelledDeadline
	case protocol.FailureProtocolEarlyEOF:
		if downstreamCommitted || failure.OutputVisible {
			return telemetry.AttemptFailedPartial
		}
		return telemetry.AttemptEarlyEOF
	default:
		if downstreamCommitted || failure.OutputVisible || failure.ToolActionable {
			return telemetry.AttemptFailedPartial
		}
		return telemetry.AttemptFailedPreOutput
	}
}

func usageEvidence(value protocol.Optional[protocol.CanonicalUsage]) *telemetry.UsageEvidence {
	usage, present := value.Get()
	if !present {
		return nil
	}
	return &telemetry.UsageEvidence{
		InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens, TotalTokens: usage.TotalTokens,
		Provenance: usage.Provenance, ProviderReported: usage.Provenance == protocol.UsageProviderReported,
	}
}

func nonnegativeDuration(start, end time.Time) time.Duration {
	if end.Before(start) {
		return 0
	}
	return end.Sub(start)
}

type discardEvidence struct{}

func (discardEvidence) RecordRequest(telemetry.RequestEvidence)   {}
func (discardEvidence) RecordDecision(telemetry.DecisionEvidence) {}
func (discardEvidence) RecordAttempt(telemetry.AttemptEvidence)   {}
