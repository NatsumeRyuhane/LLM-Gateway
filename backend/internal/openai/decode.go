package openai

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"time"

	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/protocol"
)

const absoluteMaxRequestBodyBytes = 64 << 20

// RequestMetadata is gateway-owned context required by the canonical protocol.
// It is supplied by lifecycle middleware, never decoded from the JSON body.
type RequestMetadata struct {
	RequestID string
	Deadline  time.Time
}

// ModelsRequest is the strictly decoded public models-list request. Identity
// authorization and the visible model set remain the caller's responsibility.
type ModelsRequest struct {
	RequestID   string
	Attribution protocol.Attribution
}

// Codec translates the accepted public v0 wire surface into canonical values.
type Codec struct {
	limits       protocol.Limits
	maxBodyBytes int64
}

// NewCodec constructs a bounded codec from the canonical protocol limits.
func NewCodec(limits protocol.Limits) Codec {
	maximum := int64(limits.MaxRequestContentBytes) + int64(limits.MaxTools)*(int64(limits.MaxSchemaBytes)+int64(limits.MaxToolDescriptionBytes)+512)
	if maximum <= 0 || maximum > absoluteMaxRequestBodyBytes {
		maximum = absoluteMaxRequestBodyBytes
	}
	return Codec{limits: limits, maxBodyBytes: maximum}
}

// DecodeModelsRequest validates the exact GET endpoint, representation, empty
// body, attribution extensions, and gateway-owned request metadata.
func (c Codec) DecodeModelsRequest(request *http.Request, metadata RequestMetadata) (ModelsRequest, *protocol.CanonicalError) {
	if err := validateEndpoint(request, http.MethodGet, ModelsPath); err != nil {
		return ModelsRequest{}, err
	}
	if _, err := SelectRepresentation(request.Header.Get("Accept"), false); err != nil {
		return ModelsRequest{}, err
	}
	if request.Body != nil && request.Body != http.NoBody {
		var one [1]byte
		if count, readErr := request.Body.Read(one[:]); count != 0 || (readErr != nil && !errors.Is(readErr, io.EOF)) {
			return ModelsRequest{}, invalidRequest("body", "must be empty for GET /v1/models")
		}
	}
	attribution, err := decodeAttribution(request.Header, c.limits)
	if err != nil {
		return ModelsRequest{}, err
	}
	if metadata.RequestID == "" {
		return ModelsRequest{}, invalidRequest("request_id", "is required")
	}
	return ModelsRequest{RequestID: metadata.RequestID, Attribution: attribution}, nil
}

// DecodeChatCompletions validates and normalizes one strict public v0 request.
// The returned value is already snapshotted by the canonical protocol core.
func (c Codec) DecodeChatCompletions(request *http.Request, metadata RequestMetadata) (protocol.ValidatedChatRequest, *protocol.CanonicalError) {
	if err := validateEndpoint(request, http.MethodPost, ChatCompletionsPath); err != nil {
		return protocol.ValidatedChatRequest{}, err
	}
	if err := validateJSONContentType(request.Header.Get("Content-Type")); err != nil {
		return protocol.ValidatedChatRequest{}, err
	}
	if request.Body == nil {
		return protocol.ValidatedChatRequest{}, invalidRequest("body", "is required")
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, c.maxBodyBytes+1))
	if err != nil {
		return protocol.ValidatedChatRequest{}, invalidRequest("body", "could not be read")
	}
	if len(body) == 0 {
		return protocol.ValidatedChatRequest{}, invalidRequest("body", "is required")
	}
	if int64(len(body)) > c.maxBodyBytes {
		return protocol.ValidatedChatRequest{}, invalidRequest("body", "exceeds the request-body byte limit")
	}
	if violation := validateJSONDocument(body); violation != nil {
		return protocol.ValidatedChatRequest{}, invalidRequest(violation.path, violation.rule)
	}
	canonical, decodeErr := c.decodeChatBody(body, metadata)
	if decodeErr != nil {
		return protocol.ValidatedChatRequest{}, decodeErr
	}
	if _, acceptErr := SelectRepresentation(request.Header.Get("Accept"), canonical.Stream); acceptErr != nil {
		return protocol.ValidatedChatRequest{}, acceptErr
	}
	attribution, attributionErr := decodeAttribution(request.Header, c.limits)
	if attributionErr != nil {
		return protocol.ValidatedChatRequest{}, attributionErr
	}
	canonical.Attribution = attribution
	return protocol.ValidateChatRequest(canonical, c.limits)
}

var chatFields = map[string]struct{}{
	"model": {}, "messages": {}, "stream": {}, "stream_options": {},
	"tools": {}, "tool_choice": {}, "parallel_tool_calls": {}, "response_format": {},
	"temperature": {}, "top_p": {}, "seed": {}, "stop": {},
	"max_completion_tokens": {}, "max_tokens": {}, "n": {},
}

var deferredChatFields = map[string]struct{}{
	"frequency_penalty": {}, "presence_penalty": {}, "logit_bias": {}, "logprobs": {},
	"top_logprobs": {}, "modalities": {}, "audio": {}, "prediction": {},
	"reasoning_effort": {}, "service_tier": {}, "store": {}, "metadata": {},
	"user": {}, "safety_identifier": {}, "web_search_options": {},
	"functions": {}, "function_call": {},
}

func (c Codec) decodeChatBody(body []byte, metadata RequestMetadata) (protocol.CanonicalChatRequest, *protocol.CanonicalError) {
	object, violation := decodeObject(body, "body")
	if violation != nil {
		return protocol.CanonicalChatRequest{}, invalidRequest(violation.path, violation.rule)
	}
	fields := make([]string, 0, len(object))
	for field := range object {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	for _, field := range fields {
		if _, deferred := deferredChatFields[field]; deferred {
			return protocol.CanonicalChatRequest{}, unsupported(field, "is deferred or unsupported in gateway.adapter.v0")
		}
	}
	if violation := rejectUnknownFields(object, "", chatFields); violation != nil {
		return protocol.CanonicalChatRequest{}, invalidRequest(violation.path, violation.rule)
	}
	model, violation := decodeRequiredString(object, "model", "")
	if violation != nil {
		return protocol.CanonicalChatRequest{}, invalidRequest(violation.path, violation.rule)
	}
	messagesRaw, ok := object["messages"]
	if !ok {
		return protocol.CanonicalChatRequest{}, invalidRequest("messages", "is required")
	}
	messages, err := c.decodeMessages(messagesRaw)
	if err != nil {
		return protocol.CanonicalChatRequest{}, err
	}

	canonical := protocol.CanonicalChatRequest{
		ContractVersion: protocol.ContractVersion,
		RequestID:       metadata.RequestID,
		Target:          model,
		Messages:        messages,
		ToolChoice:      protocol.ToolChoice{Kind: protocol.ToolChoiceNone},
		ResponseFormat:  protocol.ResponseFormat{Kind: protocol.ResponseFormatText},
		Deadline:        metadata.Deadline,
	}
	if raw, present := object["stream"]; present {
		if err := json.Unmarshal(raw, &canonical.Stream); err != nil {
			return protocol.CanonicalChatRequest{}, invalidRequest("stream", "must be a boolean")
		}
	}
	if raw, present := object["stream_options"]; present {
		includeUsage, err := decodeStreamOptions(raw)
		if err != nil {
			return protocol.CanonicalChatRequest{}, err
		}
		if !canonical.Stream {
			return protocol.CanonicalChatRequest{}, invalidRequest("stream_options", "requires stream to be true")
		}
		canonical.IncludeUsage = includeUsage
	}
	if raw, present := object["tools"]; present {
		tools, err := c.decodeTools(raw)
		if err != nil {
			return protocol.CanonicalChatRequest{}, err
		}
		canonical.Tools = tools
		if len(tools) != 0 {
			canonical.ToolChoice.Kind = protocol.ToolChoiceAuto
		}
	}
	if raw, present := object["tool_choice"]; present {
		choice, err := decodeToolChoice(raw)
		if err != nil {
			return protocol.CanonicalChatRequest{}, err
		}
		canonical.ToolChoice = choice
	}
	if raw, present := object["parallel_tool_calls"]; present {
		value, err := decodeBool(raw, "parallel_tool_calls")
		if err != nil {
			return protocol.CanonicalChatRequest{}, err
		}
		canonical.ParallelToolCalls = protocol.Some(value)
	}
	if raw, present := object["response_format"]; present {
		format, err := decodeResponseFormat(raw)
		if err != nil {
			return protocol.CanonicalChatRequest{}, err
		}
		canonical.ResponseFormat = format
	}
	if raw, present := object["temperature"]; present {
		value, err := decodeFloat(raw, "temperature")
		if err != nil {
			return protocol.CanonicalChatRequest{}, err
		}
		canonical.Sampling.Temperature = protocol.Some(value)
	}
	if raw, present := object["top_p"]; present {
		value, err := decodeFloat(raw, "top_p")
		if err != nil {
			return protocol.CanonicalChatRequest{}, err
		}
		canonical.Sampling.TopP = protocol.Some(value)
	}
	if raw, present := object["seed"]; present {
		value, err := decodeInt64(raw, "seed")
		if err != nil {
			return protocol.CanonicalChatRequest{}, err
		}
		canonical.Sampling.Seed = protocol.Some(value)
	}
	if raw, present := object["stop"]; present {
		value, err := decodeStop(raw)
		if err != nil {
			return protocol.CanonicalChatRequest{}, err
		}
		canonical.Sampling.Stop = protocol.Some(value)
	}
	maxCompletion, completionPresent, err := decodeOptionalInt(object, "max_completion_tokens")
	if err != nil {
		return protocol.CanonicalChatRequest{}, err
	}
	maxTokens, tokensPresent, err := decodeOptionalInt(object, "max_tokens")
	if err != nil {
		return protocol.CanonicalChatRequest{}, err
	}
	if completionPresent && tokensPresent {
		return protocol.CanonicalChatRequest{}, invalidRequest("max_tokens", "cannot be combined with max_completion_tokens")
	}
	if completionPresent {
		canonical.MaxOutputTokens = protocol.Some(maxCompletion)
	} else if tokensPresent {
		canonical.MaxOutputTokens = protocol.Some(maxTokens)
	}
	if raw, present := object["n"]; present {
		value, err := decodeInt(raw, "n")
		if err != nil {
			return protocol.CanonicalChatRequest{}, err
		}
		if value != 1 {
			return protocol.CanonicalChatRequest{}, invalidRequest("n", "must equal 1")
		}
	}
	return canonical, nil
}

func decodeStreamOptions(raw []byte) (bool, *protocol.CanonicalError) {
	object, violation := decodeObject(raw, "stream_options")
	if violation != nil {
		return false, invalidRequest(violation.path, violation.rule)
	}
	if violation := rejectUnknownFields(object, "stream_options", map[string]struct{}{"include_usage": {}}); violation != nil {
		return false, invalidRequest(violation.path, violation.rule)
	}
	includeUsage, present := object["include_usage"]
	if !present {
		return false, invalidRequest("stream_options.include_usage", "is required when stream_options is present")
	}
	return decodeBool(includeUsage, "stream_options.include_usage")
}

func decodeBool(raw []byte, path string) (bool, *protocol.CanonicalError) {
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, invalidRequest(path, "must be a boolean")
	}
	return value, nil
}

func decodeFloat(raw []byte, path string) (float64, *protocol.CanonicalError) {
	var value float64
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, invalidRequest(path, "must be a number")
	}
	return value, nil
}

func decodeInt(raw []byte, path string) (int, *protocol.CanonicalError) {
	var value int
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, invalidRequest(path, "must be an integer")
	}
	return value, nil
}

func decodeInt64(raw []byte, path string) (int64, *protocol.CanonicalError) {
	var value int64
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, invalidRequest(path, "must be an integer")
	}
	return value, nil
}

func decodeOptionalInt(object map[string]json.RawMessage, key string) (int, bool, *protocol.CanonicalError) {
	raw, present := object[key]
	if !present {
		return 0, false, nil
	}
	value, err := decodeInt(raw, key)
	return value, true, err
}

func decodeStop(raw []byte) ([]string, *protocol.CanonicalError) {
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return []string{single}, nil
	}
	var multiple []string
	if err := json.Unmarshal(raw, &multiple); err != nil || multiple == nil {
		return nil, invalidRequest("stop", "must be a string or string array")
	}
	return multiple, nil
}
