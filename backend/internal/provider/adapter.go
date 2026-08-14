package provider

import (
	"context"

	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/protocol"
)

// Attempt identifies one already-selected route attempt. Adapters neither
// select routes nor decide whether another attempt is allowed.
type Attempt struct {
	ID string
}

// EventSink accepts one validated canonical event. Returning a failure stops
// upstream processing and transfers that consumer-owned failure unchanged.
type EventSink func(protocol.CanonicalEvent) *protocol.CanonicalError

// Adapter is the narrow contract consumed by the attempt orchestrator.
// Implementations own every upstream response body for the full call.
type Adapter interface {
	Buffered(context.Context, Attempt, protocol.ValidatedChatRequest, ValidatedRoute) (protocol.ValidatedChatResponse, *protocol.CanonicalError)
	Stream(context.Context, Attempt, protocol.ValidatedChatRequest, ValidatedRoute, EventSink) (protocol.StreamResult, *protocol.CanonicalError)
}
