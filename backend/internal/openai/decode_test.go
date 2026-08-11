package openai

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/protocol"
)

var testMetadata = RequestMetadata{RequestID: "req_test", Deadline: time.Unix(2_000_000_000, 0)}

func TestDecodeChatRequestPreservesExplicitSemantics(t *testing.T) {
	body := `{
      "model":"agent",
      "messages":[
        {"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"query\":\"x\"}"}}]},
        {"role":"tool","tool_call_id":"call_1","content":"result"},
        {"role":"user","name":"caller","content":[{"type":"text","text":"next"}]}
      ],
      "stream":true,
      "stream_options":{"include_usage":true},
      "tools":[{"type":"function","function":{"name":"lookup","description":"Lookup","parameters":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"],"additionalProperties":false},"strict":true}}],
      "tool_choice":{"type":"function","function":{"name":"lookup"}},
      "parallel_tool_calls":false,
      "response_format":{"type":"json_schema","json_schema":{"schema":{"type":"object","additionalProperties":false},"strict":true}},
      "temperature":0,
      "top_p":0.8,
      "seed":0,
      "stop":"END",
      "max_completion_tokens":256,
      "n":1
    }`
	request := newChatRequest(body)
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Add(HeaderConversationID, " conv-1 ")
	request.Header.Add(HeaderConversationID, "conv-1")
	request.Header.Set(HeaderRunID, "run-1")

	validated, err := NewCodec(protocol.DefaultLimits()).DecodeChatCompletions(request, testMetadata)
	if err != nil {
		t.Fatalf("DecodeChatCompletions() error = %v", err)
	}
	canonical := validated.Canonical()
	if !canonical.Stream || !canonical.IncludeUsage {
		t.Fatalf("stream semantics = (%v, %v), want true,true", canonical.Stream, canonical.IncludeUsage)
	}
	if value, explicit := canonical.ParallelToolCalls.Get(); !explicit || value {
		t.Fatalf("parallel_tool_calls = (%v, %v), want false,true", value, explicit)
	}
	if value, explicit := canonical.Sampling.Temperature.Get(); !explicit || value != 0 {
		t.Fatalf("temperature = (%v, %v), want 0,true", value, explicit)
	}
	if value, explicit := canonical.Sampling.Seed.Get(); !explicit || value != 0 {
		t.Fatalf("seed = (%v, %v), want 0,true", value, explicit)
	}
	if value, explicit := canonical.MaxOutputTokens.Get(); !explicit || value != 256 {
		t.Fatalf("max_output_tokens = (%v, %v), want 256,true", value, explicit)
	}
	if value, explicit := canonical.Attribution.ConversationID.Get(); !explicit || value != "conv-1" {
		t.Fatalf("conversation attribution = (%q, %v), want conv-1,true", value, explicit)
	}
	if canonical.ToolChoice.Kind != protocol.ToolChoiceSpecific || canonical.ToolChoice.FunctionName != "lookup" {
		t.Fatalf("tool choice = %#v", canonical.ToolChoice)
	}
	wantCapabilities := []protocol.Capability{
		protocol.CapabilityContentText,
		protocol.CapabilityEndpointStreaming,
		protocol.CapabilityParticipantName,
		protocol.CapabilityRoleAssistant,
		protocol.CapabilityRoleTool,
		protocol.CapabilityRoleUser,
		protocol.CapabilityParameterMaxOutputTokens,
		protocol.CapabilityParameterSeed,
		protocol.CapabilityParameterStop,
		protocol.CapabilityParameterTemperature,
		protocol.CapabilityParameterTopP,
		protocol.CapabilityStructuredJSONSchema,
		protocol.CapabilityStructuredJSONStrict,
		protocol.CapabilityStructuredStreaming,
		protocol.CapabilityToolsChoiceSpecific,
		protocol.CapabilityToolsFunction,
		protocol.CapabilityToolsFunctionSchemaStrict,
		protocol.CapabilityToolsParallel,
		protocol.CapabilityUsageStreaming,
	}
	if got := validated.RequiredCapabilities(); !reflect.DeepEqual(got, wantCapabilities) {
		t.Fatalf("required capabilities = %#v, want %#v", got, wantCapabilities)
	}
}

func TestChatRequestDecodingGoldenFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "tests", "conformance", "gateway.adapter.v0", "http", "chat-request-decoding.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		ContractVersion string `json:"contract_version"`
		Cases           []struct {
			Name     string          `json:"name"`
			Body     json.RawMessage `json:"body"`
			Expected struct {
				Target          string   `json:"target"`
				Content         []string `json:"content"`
				Stream          bool     `json:"stream"`
				MaxOutputTokens *int     `json:"max_output_tokens"`
				Failure         *struct {
					Code  protocol.FailureCode `json:"code"`
					Param string               `json:"param"`
				} `json:"failure"`
			} `json:"expected"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.ContractVersion != protocol.ContractVersion {
		t.Fatalf("contract_version = %q", fixture.ContractVersion)
	}
	codec := NewCodec(protocol.DefaultLimits())
	for _, testCase := range fixture.Cases {
		t.Run(testCase.Name, func(t *testing.T) {
			validated, decodeErr := codec.DecodeChatCompletions(newChatRequest(string(testCase.Body)), testMetadata)
			if testCase.Expected.Failure != nil {
				if decodeErr == nil {
					t.Fatal("DecodeChatCompletions() error = nil")
				}
				if decodeErr.Code != testCase.Expected.Failure.Code {
					t.Fatalf("failure code = %q, want %q", decodeErr.Code, testCase.Expected.Failure.Code)
				}
				if decodeErr.Validation == nil || decodeErr.Validation.Path != testCase.Expected.Failure.Param {
					t.Fatalf("failure param = %#v, want %q", decodeErr.Validation, testCase.Expected.Failure.Param)
				}
				return
			}
			if decodeErr != nil {
				t.Fatalf("DecodeChatCompletions() error = %v", decodeErr)
			}
			canonical := validated.Canonical()
			if canonical.Target != testCase.Expected.Target || canonical.Stream != testCase.Expected.Stream {
				t.Fatalf("canonical target/stream = %q/%v", canonical.Target, canonical.Stream)
			}
			gotContent := make([]string, len(canonical.Messages[0].Content))
			for index, part := range canonical.Messages[0].Content {
				gotContent[index] = part.Text
			}
			if !reflect.DeepEqual(gotContent, testCase.Expected.Content) {
				t.Fatalf("content = %#v, want %#v", gotContent, testCase.Expected.Content)
			}
			if testCase.Expected.MaxOutputTokens != nil {
				value, explicit := canonical.MaxOutputTokens.Get()
				if !explicit || value != *testCase.Expected.MaxOutputTokens {
					t.Fatalf("max_output_tokens = (%d,%v)", value, explicit)
				}
			}
		})
	}
}

func TestDecodeRejectsAmbiguousAndUnknownInput(t *testing.T) {
	tests := []struct {
		name string
		body string
		code protocol.FailureCode
		path string
	}{
		{"duplicate field", `{"model":"a","model":"b","messages":[{"role":"user","content":"x"}]}`, protocol.FailureClientInvalidRequest, "model"},
		{"unknown message field", `{"model":"a","messages":[{"role":"user","content":"x","extra":true}]}`, protocol.FailureClientInvalidRequest, "messages[0].extra"},
		{"refusal history", `{"model":"a","messages":[{"role":"assistant","content":"x","refusal":"no"}]}`, protocol.FailureCapabilityUnsupported, "messages[0].refusal"},
		{"known deferred field", `{"model":"a","messages":[{"role":"user","content":"x"}],"metadata":{}}`, protocol.FailureCapabilityUnsupported, "metadata"},
		{"stream options in buffered mode", `{"model":"a","messages":[{"role":"user","content":"x"}],"stream_options":{"include_usage":true}}`, protocol.FailureClientInvalidRequest, "stream_options"},
	}
	codec := NewCodec(protocol.DefaultLimits())
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := codec.DecodeChatCompletions(newChatRequest(testCase.body), testMetadata)
			if err == nil || err.Code != testCase.code || err.Validation == nil || err.Validation.Path != testCase.path {
				t.Fatalf("error = %#v, want code=%q path=%q", err, testCase.code, testCase.path)
			}
		})
	}
}

func TestDecodeModelsRequest(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, ModelsPath, nil)
	request.Header.Set("Accept", "application/json")
	request.Header.Set(HeaderConversationID, "conversation")
	decoded, err := NewCodec(protocol.DefaultLimits()).DecodeModelsRequest(request, testMetadata)
	if err != nil {
		t.Fatalf("DecodeModelsRequest() error = %v", err)
	}
	if value, present := decoded.Attribution.ConversationID.Get(); !present || value != "conversation" {
		t.Fatalf("conversation = (%q,%v)", value, present)
	}
}

func FuzzDecodeChatCompletionsNeverPanics(f *testing.F) {
	f.Add([]byte(`{"model":"a","messages":[{"role":"user","content":"hello"}]}`))
	f.Add([]byte(`{"model":"a","messages":[{"role":"user","content":[{"type":"image_url"}]}]}`))
	codec := NewCodec(protocol.DefaultLimits())
	f.Fuzz(func(t *testing.T, body []byte) {
		request := httptest.NewRequest(http.MethodPost, ChatCompletionsPath, bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		_, _ = codec.DecodeChatCompletions(request, testMetadata)
	})
}

func newChatRequest(body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, ChatCompletionsPath, bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	return request
}
