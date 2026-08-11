package protocol

import (
	"encoding/json"
	"time"
)

const ContractVersion = "gateway.adapter.v0"

type MessageRole string

const (
	RoleDeveloper MessageRole = "developer"
	RoleSystem    MessageRole = "system"
	RoleUser      MessageRole = "user"
	RoleAssistant MessageRole = "assistant"
	RoleTool      MessageRole = "tool"
)

type ContentType string

const ContentText ContentType = "text"

type CanonicalContentPart struct {
	Type ContentType
	Text string
}

type CanonicalToolCall struct {
	ID        string
	Name      string
	Arguments string
}

type CanonicalMessage struct {
	Role       MessageRole
	Name       Optional[string]
	Content    []CanonicalContentPart
	Refusal    Optional[string]
	ToolCalls  []CanonicalToolCall
	ToolCallID Optional[string]
}

// JSONSchema stores an exact JSON Schema document. Construction and access copy
// bytes so a validated request cannot be mutated through a caller-owned slice.
type JSONSchema struct {
	raw json.RawMessage
}

func NewJSONSchema(raw []byte) JSONSchema {
	return JSONSchema{raw: append(json.RawMessage(nil), raw...)}
}

func (s JSONSchema) Bytes() []byte {
	return append([]byte(nil), s.raw...)
}

type CanonicalFunctionTool struct {
	Name        string
	Description Optional[string]
	Parameters  JSONSchema
	Strict      Optional[bool]
}

type ToolChoiceKind string

const (
	ToolChoiceNone     ToolChoiceKind = "none"
	ToolChoiceAuto     ToolChoiceKind = "auto"
	ToolChoiceRequired ToolChoiceKind = "required"
	ToolChoiceSpecific ToolChoiceKind = "function"
)

type ToolChoice struct {
	Kind         ToolChoiceKind
	FunctionName string
}

type ResponseFormatKind string

const (
	ResponseFormatText       ResponseFormatKind = "text"
	ResponseFormatJSONObject ResponseFormatKind = "json_object"
	ResponseFormatJSONSchema ResponseFormatKind = "json_schema"
)

type ResponseFormat struct {
	Kind   ResponseFormatKind
	Schema JSONSchema
	Strict Optional[bool]
}

type SamplingParameters struct {
	Temperature Optional[float64]
	TopP        Optional[float64]
	Seed        Optional[int64]
	Stop        Optional[[]string]
}

type Attribution struct {
	ConversationID Optional[string]
	RunID          Optional[string]
}

// CanonicalChatRequest is the provider-neutral input produced by a public codec.
// Call ValidateChatRequest before passing it to routing or an adapter.
type CanonicalChatRequest struct {
	ContractVersion   string
	RequestID         string
	Target            string
	Messages          []CanonicalMessage
	Tools             []CanonicalFunctionTool
	ToolChoice        ToolChoice
	ParallelToolCalls Optional[bool]
	ResponseFormat    ResponseFormat
	Sampling          SamplingParameters
	MaxOutputTokens   Optional[int]
	Stream            bool
	IncludeUsage      bool
	Attribution       Attribution
	Deadline          time.Time
}

type FinishReason string

const (
	FinishStop          FinishReason = "stop"
	FinishLength        FinishReason = "length"
	FinishToolCalls     FinishReason = "tool_calls"
	FinishContentFilter FinishReason = "content_filter"
)

type UsageProvenance string

const (
	UsageProviderReported UsageProvenance = "provider_reported"
	UsageGatewayEstimated UsageProvenance = "gateway_estimated"
	UsageUnavailable      UsageProvenance = "unavailable"
)

type CanonicalUsage struct {
	InputTokens     int64
	OutputTokens    int64
	TotalTokens     int64
	CachedTokens    Optional[int64]
	ReasoningTokens Optional[int64]
	Provenance      UsageProvenance
	Partial         bool
}

type CanonicalChatResponse struct {
	ResponseID   string
	RequestID    string
	AttemptID    string
	RouteID      string
	Model        string
	CreatedAt    time.Time
	Message      CanonicalMessage
	FinishReason FinishReason
	Usage        Optional[CanonicalUsage]
}
