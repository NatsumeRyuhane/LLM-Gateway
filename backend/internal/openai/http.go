package openai

import (
	"fmt"
	"mime"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/protocol"
)

const (
	ModelsPath                 = "/v1/models"
	ChatCompletionsPath        = "/v1/chat/completions"
	MediaTypeJSON              = "application/json"
	MediaTypeEventStream       = "text/event-stream"
	HeaderRequestID            = "X-Gateway-Request-ID"
	HeaderAttemptID            = "X-Gateway-Attempt-ID"
	HeaderRouteID              = "X-Gateway-Route-ID"
	HeaderConversationID       = "X-Gateway-Conversation-ID"
	HeaderRunID                = "X-Gateway-Run-ID"
	maxAcceptHeaderBytes       = 8 << 10
	maxAttributionHeaderValues = 16
)

var rejectedAccountHeaders = []string{"OpenAI-Organization", "OpenAI-Project"}
var qualityPattern = regexp.MustCompile(`^(?:0(?:\.[0-9]{0,3})?|1(?:\.0{0,3})?)$`)

func validateEndpoint(request *http.Request, method, path string) *protocol.CanonicalError {
	if request == nil {
		return invalidRequest("request", "is required")
	}
	if request.Method != method {
		return invalidRequest("method", "is not supported for this endpoint")
	}
	if request.URL == nil || request.URL.Path != path || request.URL.RawPath != "" || request.URL.RawQuery != "" || request.URL.ForceQuery {
		return invalidRequest("path", "must match the endpoint exactly without a query string")
	}
	for _, header := range rejectedAccountHeaders {
		if len(request.Header.Values(header)) != 0 {
			return unsupported("headers."+strings.ToLower(header), "OpenAI account-selection headers are not supported")
		}
	}
	return nil
}

func validateJSONContentType(header string) *protocol.CanonicalError {
	if header == "" {
		return invalidRequest("headers.content-type", "application/json is required")
	}
	mediaType, parameters, err := mime.ParseMediaType(header)
	if err != nil || !strings.EqualFold(mediaType, MediaTypeJSON) || len(parameters) != 0 {
		return invalidRequest("headers.content-type", "must be application/json without parameters")
	}
	return nil
}

// SelectRepresentation validates the bounded v0 Accept grammar and returns the
// body-selected representation. The Accept header never overrides stream.
func SelectRepresentation(header string, stream bool) (string, *protocol.CanonicalError) {
	selected := MediaTypeJSON
	if stream {
		selected = MediaTypeEventStream
	}
	if strings.TrimSpace(header) == "" {
		return selected, nil
	}
	if len(header) > maxAcceptHeaderBytes {
		return "", invalidRequest("headers.accept", "exceeds the header byte limit")
	}

	type preference struct {
		specificity int
		quality     float64
	}
	preferences := make([]preference, 0)
	for _, item := range strings.Split(header, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			return "", invalidRequest("headers.accept", "contains an empty media range")
		}
		parts := strings.Split(item, ";")
		rangeValue := strings.ToLower(strings.TrimSpace(parts[0]))
		if !acceptedMediaRange(rangeValue) {
			return "", invalidRequest("headers.accept", "contains an unregistered media range")
		}
		quality := 1.0
		if len(parts) > 2 {
			return "", invalidRequest("headers.accept", "contains unregistered parameters")
		}
		if len(parts) == 2 {
			parameter := strings.TrimSpace(parts[1])
			name, value, found := strings.Cut(parameter, "=")
			if !found || !strings.EqualFold(strings.TrimSpace(name), "q") || strings.TrimSpace(value) == "" {
				return "", invalidRequest("headers.accept", "contains an invalid quality parameter")
			}
			qualityValue := strings.TrimSpace(value)
			parsed, err := strconv.ParseFloat(qualityValue, 64)
			if err != nil || !qualityPattern.MatchString(qualityValue) {
				return "", invalidRequest("headers.accept", "contains an invalid quality value")
			}
			quality = parsed
		}
		if specificity, matches := mediaRangeSpecificity(rangeValue, selected); matches {
			preferences = append(preferences, preference{specificity: specificity, quality: quality})
		}
	}
	if len(preferences) == 0 {
		return "", invalidRequest("headers.accept", "does not accept the selected representation")
	}
	sort.Slice(preferences, func(i, j int) bool {
		if preferences[i].specificity != preferences[j].specificity {
			return preferences[i].specificity > preferences[j].specificity
		}
		return preferences[i].quality > preferences[j].quality
	})
	bestSpecificity := preferences[0].specificity
	bestQuality := preferences[0].quality
	for _, candidate := range preferences[1:] {
		if candidate.specificity != bestSpecificity {
			break
		}
		if candidate.quality > bestQuality {
			bestQuality = candidate.quality
		}
	}
	if bestQuality <= 0 {
		return "", invalidRequest("headers.accept", "does not accept the selected representation")
	}
	return selected, nil
}

func acceptedMediaRange(value string) bool {
	switch value {
	case MediaTypeJSON, MediaTypeEventStream, "application/*", "text/*", "*/*":
		return true
	default:
		return false
	}
}

func mediaRangeSpecificity(mediaRange, selected string) (int, bool) {
	if mediaRange == selected {
		return 2, true
	}
	selectedType, _, _ := strings.Cut(selected, "/")
	if mediaRange == selectedType+"/*" {
		return 1, true
	}
	if mediaRange == "*/*" {
		return 0, true
	}
	return 0, false
}

func decodeAttribution(headers http.Header, limits protocol.Limits) (protocol.Attribution, *protocol.CanonicalError) {
	conversation, err := decodeAttributionHeader(headers, HeaderConversationID, limits.MaxIdentifierBytes)
	if err != nil {
		return protocol.Attribution{}, err
	}
	run, err := decodeAttributionHeader(headers, HeaderRunID, limits.MaxIdentifierBytes)
	if err != nil {
		return protocol.Attribution{}, err
	}
	return protocol.Attribution{ConversationID: conversation, RunID: run}, nil
}

func decodeAttributionHeader(headers http.Header, name string, maximum int) (protocol.Optional[string], *protocol.CanonicalError) {
	values := headers.Values(name)
	if len(values) == 0 {
		return protocol.None[string](), nil
	}
	path := "headers." + strings.ToLower(name)
	if len(values) > maxAttributionHeaderValues {
		return protocol.None[string](), invalidRequest(path, "contains too many values")
	}
	normalized := ""
	for index, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || !utf8.ValidString(value) || len(value) > maximum || containsUnicodeControl(value) {
			return protocol.None[string](), invalidRequest(path, "must be a bounded non-empty identifier")
		}
		if index == 0 {
			normalized = value
			continue
		}
		if value != normalized {
			return protocol.None[string](), invalidRequest(path, "contains conflicting values")
		}
	}
	return protocol.Some(normalized), nil
}

func containsUnicodeControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}

func formatArrayPath(path string, index int) string {
	return fmt.Sprintf("%s[%d]", path, index)
}
