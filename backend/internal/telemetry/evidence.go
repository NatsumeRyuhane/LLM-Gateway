package telemetry

import (
	"time"

	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/protocol"
)

const (
	RequestEvidenceSchema  = "gateway.request_timing.v0"
	DecisionEvidenceSchema = "gateway.decision.v0"
	AttemptEvidenceSchema  = "gateway.attempt.v0"
)

// TrafficClass is assigned by trusted gateway composition, never by a client.
type TrafficClass string

const TrafficOrdinary TrafficClass = "ordinary"

// Operation is a fixed data-plane operation class.
type Operation string

const (
	OperationModelsList      Operation = "models.list"
	OperationChatCompletions Operation = "chat.completions"
)

// Outcome is a bounded request or decision terminal classification.
type Outcome string

const (
	OutcomeSuccess    Outcome = "success"
	OutcomeRejected   Outcome = "rejected"
	OutcomeFailed     Outcome = "failed"
	OutcomeCancelled  Outcome = "cancelled"
	OutcomePartial    Outcome = "partial"
	OutcomeIncomplete Outcome = "incomplete"
)

// AttemptTerminal is the bounded terminal vocabulary from gateway.telemetry.v0.
type AttemptTerminal string

const (
	AttemptCompleted         AttemptTerminal = "completed"
	AttemptFailedPreOutput   AttemptTerminal = "failed_pre_output"
	AttemptFailedPartial     AttemptTerminal = "failed_partial"
	AttemptCancelledClient   AttemptTerminal = "cancelled_client"
	AttemptCancelledDeadline AttemptTerminal = "cancelled_deadline"
	AttemptEarlyEOF          AttemptTerminal = "early_eof"
	AttemptOther             AttemptTerminal = "other"
)

// UsageEvidence contains only provider-neutral counts and provenance.
type UsageEvidence struct {
	InputTokens      int64
	OutputTokens     int64
	TotalTokens      int64
	Provenance       protocol.UsageProvenance
	ProviderReported bool
}

// RequestEvidence is the bounded metadata-only terminal record for one HTTP request.
// It deliberately has no fields for headers, credentials, endpoints, bodies, or raw errors.
type RequestEvidence struct {
	SchemaVersion   string
	RequestID       string
	DecisionID      string
	OccurredAt      time.Time
	TrafficClass    TrafficClass
	Operation       Operation
	Stream          bool
	RequestedTarget string
	Admitted        bool
	Outcome         Outcome
	FailureDomain   protocol.FailureDomain
	FailureCode     protocol.FailureCode
	TotalLatency    time.Duration
	DispatchLatency time.Duration
	Usage           *UsageEvidence
}

// DecisionEvidence reconstructs the deliberately trivial v0 single-route decision.
type DecisionEvidence struct {
	SchemaVersion   string
	DecisionID      string
	RequestID       string
	OccurredAt      time.Time
	TrafficClass    TrafficClass
	ApplicationID   string
	RequestedTarget string
	SelectedRouteID string
	AttemptID       string
	Outcome         Outcome
	FailureCode     protocol.FailureCode
}

// AttemptEvidence records one and only one upstream dispatch. Canonical output
// acceptance and the downstream commit boundary are intentionally independent.
type AttemptEvidence struct {
	SchemaVersion           string
	AttemptID               string
	DecisionID              string
	RequestID               string
	Ordinal                 int
	RouteID                 string
	StartedAt               time.Time
	EndedAt                 time.Time
	CanonicalAcceptedAt     time.Time
	DownstreamCommittedAt   time.Time
	CanonicalOutputAccepted bool
	DownstreamCommitted     bool
	ToolActionable          bool
	Terminal                AttemptTerminal
	FailureCode             protocol.FailureCode
	RetryDisposition        protocol.RetryDisposition
	ProviderStatus          int
	TotalLatency            time.Duration
	TimeToCanonicalOutput   time.Duration
	TimeToDownstreamCommit  time.Duration
	Usage                   *UsageEvidence
}
