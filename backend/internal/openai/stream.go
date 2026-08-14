package openai

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/protocol"
)

// StreamMetadata contains gateway-owned stable Chat Completion chunk fields.
type StreamMetadata struct {
	ResponseID string
	CreatedAt  time.Time
}

// StreamEncoder validates exactly one canonical attempt and serializes accepted
// events in order. It never serializes bytes from a different attempt and never
// emits the success sentinel before validated canonical completion.
type StreamEncoder struct {
	validator *protocol.StreamValidator
	request   protocol.CanonicalChatRequest
	metadata  StreamMetadata
	header    http.Header
	roleSent  bool
}

// NewStreamEncoder creates a per-attempt encoder. A new instance is required for
// every retry so attempt and route identities cannot be mixed.
func (c Codec) NewStreamEncoder(
	request protocol.ValidatedChatRequest,
	attemptID string,
	routeID string,
	metadata StreamMetadata,
	visibility CorrelationVisibility,
) (*StreamEncoder, *protocol.CanonicalError) {
	canonical := request.Canonical()
	if !validPublicIdentifier(metadata.ResponseID, c.limits.MaxIdentifierBytes) {
		return nil, invalidRequest("response_id", "must be a bounded public identifier")
	}
	if metadata.CreatedAt.IsZero() {
		return nil, invalidRequest("created_at", "must be set")
	}
	validator, err := protocol.NewStreamValidator(request, attemptID, routeID)
	if err != nil {
		return nil, err
	}
	header := c.responseHeaders(MediaTypeEventStream, canonical.RequestID, attemptID, routeID, visibility)
	header.Set("Cache-Control", "no-cache")
	return &StreamEncoder{validator: validator, request: canonical, metadata: metadata, header: header}, nil
}

// Header returns a copy of the headers to commit with the first encoded model
// frame. Callers must not commit them before a non-empty Encode result.
func (e *StreamEncoder) Header() http.Header {
	if e == nil {
		return nil
	}
	return e.header.Clone()
}

// Encode validates one event atomically and returns zero or more complete SSE
// frames. Valid failure/cancellation terminals are returned as canonical errors
// with no false success frame.
func (e *StreamEncoder) Encode(event protocol.CanonicalEvent) ([]byte, *protocol.CanonicalError) {
	if e == nil || e.validator == nil {
		return nil, encodingFailure("")
	}
	if err := e.validator.Accept(event); err != nil {
		return nil, err
	}
	switch event.Type {
	case protocol.EventResponseStarted, protocol.EventUsageUpdated, protocol.EventToolCallCompleted:
		return nil, nil
	case protocol.EventOutputTextDelta:
		content := event.Text
		return e.encodeChoiceDelta(chunkDelta{Role: e.nextRole(), Content: &content}, nil)
	case protocol.EventRefusalDelta:
		refusal := event.Text
		return e.encodeChoiceDelta(chunkDelta{Role: e.nextRole(), Refusal: &refusal}, nil)
	case protocol.EventToolCallStarted:
		identifier := event.ToolCallID
		kind := "function"
		name := event.ToolName
		emptyArguments := ""
		return e.encodeChoiceDelta(chunkDelta{
			Role: e.nextRole(),
			ToolCalls: []chunkToolCall{{
				Index: event.ToolCallIndex, ID: &identifier, Type: &kind,
				Function: chunkFunctionCall{Name: &name, Arguments: &emptyArguments},
			}},
		}, nil)
	case protocol.EventToolCallArgumentsDelta:
		arguments := event.Text
		return e.encodeChoiceDelta(chunkDelta{
			ToolCalls: []chunkToolCall{{Index: event.ToolCallIndex, Function: chunkFunctionCall{Arguments: &arguments}}},
		}, nil)
	case protocol.EventResponseCompleted:
		result, err := e.validator.Result()
		if err != nil {
			return nil, err
		}
		finish := string(result.FinishReason)
		frames, encodeErr := e.encodeChoiceDelta(chunkDelta{}, &finish)
		if encodeErr != nil {
			return nil, encodeErr
		}
		if e.request.IncludeUsage {
			if usage, present := result.Usage.Get(); present {
				usageFrame, err := e.encodeChunk(streamChunk{Choices: []chunkChoice{}, Usage: encodeUsage(usage)})
				if err != nil {
					return nil, err
				}
				frames = append(frames, usageFrame...)
			}
		}
		frames = append(frames, []byte("data: [DONE]\n\n")...)
		return frames, nil
	case protocol.EventResponseFailed, protocol.EventResponseCancelled:
		_, terminal := e.validator.Result()
		return nil, terminal
	default:
		return nil, encodingFailure(e.request.RequestID)
	}
}

// FinalizeEOF reports incomplete transport termination without serializing a
// success sentinel. It is nil only when a terminal event was already accepted.
func (e *StreamEncoder) FinalizeEOF() *protocol.CanonicalError {
	if e == nil || e.validator == nil {
		return encodingFailure("")
	}
	return e.validator.FinalizeEOF()
}

// Successful reports whether response.completed was validated and encoded.
func (e *StreamEncoder) Successful() bool {
	return e != nil && e.validator != nil && e.validator.Successful()
}

// OutputVisible reports canonical model-output visibility for retry decisions.
func (e *StreamEncoder) OutputVisible() bool {
	return e != nil && e.validator != nil && e.validator.OutputVisible()
}

type streamChunk struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []chunkChoice  `json:"choices"`
	Usage   *usageEnvelope `json:"usage,omitempty"`
}

type chunkChoice struct {
	Index        int        `json:"index"`
	Delta        chunkDelta `json:"delta"`
	FinishReason *string    `json:"finish_reason"`
}

type chunkDelta struct {
	Role      string          `json:"role,omitempty"`
	Content   *string         `json:"content,omitempty"`
	Refusal   *string         `json:"refusal,omitempty"`
	ToolCalls []chunkToolCall `json:"tool_calls,omitempty"`
}

type chunkToolCall struct {
	Index    int               `json:"index"`
	ID       *string           `json:"id,omitempty"`
	Type     *string           `json:"type,omitempty"`
	Function chunkFunctionCall `json:"function"`
}

type chunkFunctionCall struct {
	Name      *string `json:"name,omitempty"`
	Arguments *string `json:"arguments,omitempty"`
}

func (e *StreamEncoder) nextRole() string {
	if e.roleSent {
		return ""
	}
	e.roleSent = true
	return "assistant"
}

func (e *StreamEncoder) encodeChoiceDelta(delta chunkDelta, finish *string) ([]byte, *protocol.CanonicalError) {
	return e.encodeChunk(streamChunk{Choices: []chunkChoice{{Index: 0, Delta: delta, FinishReason: finish}}})
}

func (e *StreamEncoder) encodeChunk(chunk streamChunk) ([]byte, *protocol.CanonicalError) {
	chunk.ID = e.metadata.ResponseID
	chunk.Object = "chat.completion.chunk"
	chunk.Created = e.metadata.CreatedAt.Unix()
	chunk.Model = e.request.Target
	payload, err := json.Marshal(chunk)
	if err != nil {
		return nil, encodingFailure(e.request.RequestID)
	}
	frame := make([]byte, 0, len(payload)+8)
	frame = append(frame, "data: "...)
	frame = append(frame, payload...)
	frame = append(frame, '\n', '\n')
	return frame, nil
}
