package protocol

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestValidateChatRequestPreservesExplicitIntentAndDerivesCapabilities(t *testing.T) {
	t.Parallel()

	request := validRequest()
	request.Tools = []CanonicalFunctionTool{{
		Name:       "weather",
		Parameters: NewJSONSchema([]byte(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"],"additionalProperties":false}`)),
		Strict:     Some(true),
	}}
	request.ToolChoice = ToolChoice{Kind: ToolChoiceSpecific, FunctionName: "weather"}
	request.ParallelToolCalls = Some(false)
	request.ResponseFormat = ResponseFormat{
		Kind:   ResponseFormatJSONSchema,
		Schema: NewJSONSchema([]byte(`{"type":"object","properties":{"forecast":{"type":"string"}},"required":["forecast"],"additionalProperties":false}`)),
		Strict: Some(true),
	}
	request.Sampling = SamplingParameters{
		Temperature: Some(0.0),
		TopP:        Some(1.0),
		Seed:        Some(int64(0)),
		Stop:        Some([]string{"END"}),
	}
	request.MaxOutputTokens = Some(256)
	request.Stream = true
	request.IncludeUsage = true
	request.Attribution.ConversationID = Some("conversation-1")
	request.Attribution.RunID = Some("run-1")

	validated, err := ValidateChatRequest(request, DefaultLimits())
	if err != nil {
		t.Fatalf("ValidateChatRequest() error = %v", err)
	}

	wantCapabilities := []Capability{
		CapabilityContentText,
		CapabilityEndpointStreaming,
		CapabilityParameterMaxOutputTokens,
		CapabilityParameterSeed,
		CapabilityParameterStop,
		CapabilityParameterTemperature,
		CapabilityParameterTopP,
		CapabilityRoleUser,
		CapabilityStructuredJSONSchema,
		CapabilityStructuredJSONStrict,
		CapabilityStructuredStreaming,
		CapabilityToolsChoiceSpecific,
		CapabilityToolsFunction,
		CapabilityToolsFunctionSchemaStrict,
		CapabilityToolsParallel,
		CapabilityUsageStreaming,
	}
	slices.Sort(wantCapabilities)
	if got := validated.RequiredCapabilities(); !slices.Equal(got, wantCapabilities) {
		t.Fatalf("RequiredCapabilities() = %v, want %v", got, wantCapabilities)
	}

	canonical := validated.Canonical()
	parallel, explicit := canonical.ParallelToolCalls.Get()
	if !explicit || parallel {
		t.Fatalf("ParallelToolCalls = (%v, %v), want explicit false", parallel, explicit)
	}
	temperature, explicit := canonical.Sampling.Temperature.Get()
	if !explicit || temperature != 0 {
		t.Fatalf("Temperature = (%v, %v), want explicit zero", temperature, explicit)
	}
}

func TestValidatedChatRequestIsSnapshot(t *testing.T) {
	t.Parallel()

	request := validRequest()
	request.Sampling.Stop = Some([]string{"before"})
	validated, err := ValidateChatRequest(request, DefaultLimits())
	if err != nil {
		t.Fatalf("ValidateChatRequest() error = %v", err)
	}

	request.Messages[0].Content[0].Text = "mutated"
	stop, _ := request.Sampling.Stop.Get()
	stop[0] = "mutated"
	first := validated.Canonical()
	first.Messages[0].Content[0].Text = "mutated-again"
	firstStop, _ := first.Sampling.Stop.Get()
	firstStop[0] = "mutated-again"
	capabilities := validated.RequiredCapabilities()
	capabilities[0] = CapabilityGatewayTestOnly

	second := validated.Canonical()
	if got := second.Messages[0].Content[0].Text; got != "hello" {
		t.Fatalf("snapshot content = %q, want hello", got)
	}
	secondStop, _ := second.Sampling.Stop.Get()
	if got := secondStop[0]; got != "before" {
		t.Fatalf("snapshot stop = %q, want before", got)
	}
	if slices.Contains(validated.RequiredCapabilities(), CapabilityGatewayTestOnly) {
		t.Fatal("mutating returned capabilities changed validated request")
	}
}

const CapabilityGatewayTestOnly Capability = "test.invalid"

func TestValidateChatRequestRejectsInvalidSemanticsDeterministically(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		edit func(*CanonicalChatRequest)
		path string
	}{
		{"contract version", func(r *CanonicalChatRequest) { r.ContractVersion = "v1" }, "contract_version"},
		{"empty messages", func(r *CanonicalChatRequest) { r.Messages = nil }, "messages"},
		{"unknown role", func(r *CanonicalChatRequest) { r.Messages[0].Role = "function" }, "messages[0].role"},
		{"empty user content", func(r *CanonicalChatRequest) { r.Messages[0].Content = nil }, "messages[0].content"},
		{"refusal history", func(r *CanonicalChatRequest) { r.Messages[0].Refusal = Some("no") }, "messages[0].refusal"},
		{"tool id without tool role", func(r *CanonicalChatRequest) { r.Messages[0].ToolCallID = Some("call-1") }, "messages[0].tool_call_id"},
		{"invalid participant", func(r *CanonicalChatRequest) { r.Messages[0].Name = Some("bad name") }, "messages[0].name"},
		{"tool choice without tools", func(r *CanonicalChatRequest) { r.ToolChoice.Kind = ToolChoiceAuto }, "tool_choice"},
		{"parallel without tools", func(r *CanonicalChatRequest) { r.ParallelToolCalls = Some(false) }, "parallel_tool_calls"},
		{"invalid temperature", func(r *CanonicalChatRequest) { r.Sampling.Temperature = Some(3.0) }, "sampling.temperature"},
		{"empty stop", func(r *CanonicalChatRequest) { r.Sampling.Stop = Some([]string{}) }, "sampling.stop"},
		{"invalid output bound", func(r *CanonicalChatRequest) { r.MaxOutputTokens = Some(0) }, "max_output_tokens"},
		{"empty attribution", func(r *CanonicalChatRequest) { r.Attribution.RunID = Some("") }, "attribution.run_id"},
		{"control attribution", func(r *CanonicalChatRequest) { r.Attribution.ConversationID = Some("secret\nvalue") }, "attribution.conversation_id"},
		{"schema is array", func(r *CanonicalChatRequest) {
			r.Tools = []CanonicalFunctionTool{{Name: "tool", Parameters: NewJSONSchema([]byte(`[]`))}}
			r.ToolChoice.Kind = ToolChoiceAuto
		}, "tools[0].parameters"},
		{"unsupported schema keyword", func(r *CanonicalChatRequest) {
			r.Tools = []CanonicalFunctionTool{{Name: "tool", Parameters: NewJSONSchema([]byte(`{"type":"object","$ref":"#"}`))}}
			r.ToolChoice.Kind = ToolChoiceAuto
		}, "tools[0].parameters"},
		{"specific undeclared tool", func(r *CanonicalChatRequest) {
			r.Tools = []CanonicalFunctionTool{{Name: "tool", Parameters: NewJSONSchema([]byte(`{"type":"object"}`))}}
			r.ToolChoice = ToolChoice{Kind: ToolChoiceSpecific, FunctionName: "missing"}
		}, "tool_choice.function"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request := validRequest()
			test.edit(&request)
			_, first := ValidateChatRequest(request, DefaultLimits())
			_, second := ValidateChatRequest(request, DefaultLimits())
			if first == nil || second == nil {
				t.Fatal("ValidateChatRequest() error = nil, want rejection")
			}
			if first.Code != FailureClientInvalidRequest || first.Domain != DomainClient || first.RetryDisposition != RetryNever {
				t.Fatalf("error classification = (%s, %s, %s)", first.Code, first.Domain, first.RetryDisposition)
			}
			if first.Validation == nil || first.Validation.Path != test.path {
				t.Fatalf("validation path = %#v, want %q", first.Validation, test.path)
			}
			if first.Error() != second.Error() {
				t.Fatalf("non-deterministic errors: %q != %q", first, second)
			}
			if strings.Contains(first.Error(), "secret") || strings.Contains(first.Error(), "bad name") {
				t.Fatalf("safe error leaks rejected value: %q", first)
			}
		})
	}
}

func TestValidateChatRequestChecksToolHistoryAndSchema(t *testing.T) {
	t.Parallel()

	request := validRequest()
	request.Tools = []CanonicalFunctionTool{{
		Name:       "weather",
		Parameters: NewJSONSchema([]byte(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"],"additionalProperties":false}`)),
	}}
	request.ToolChoice = ToolChoice{Kind: ToolChoiceAuto}
	request.Messages = []CanonicalMessage{
		{Role: RoleUser, Content: []CanonicalContentPart{{Type: ContentText, Text: "weather?"}}},
		{Role: RoleAssistant, ToolCalls: []CanonicalToolCall{{ID: "call-1", Name: "weather", Arguments: `{"city":"Shanghai"}`}}},
		{Role: RoleTool, ToolCallID: Some("call-1"), Content: []CanonicalContentPart{{Type: ContentText, Text: "sunny"}}},
		{Role: RoleUser, Content: []CanonicalContentPart{{Type: ContentText, Text: "thanks"}}},
	}
	if _, err := ValidateChatRequest(request, DefaultLimits()); err != nil {
		t.Fatalf("ValidateChatRequest() error = %v", err)
	}

	request.Messages[1].ToolCalls[0].Arguments = `{"unknown":true}`
	_, err := ValidateChatRequest(request, DefaultLimits())
	if err == nil || err.Validation == nil || err.Validation.Path != "messages[1].tool_calls[0].arguments" {
		t.Fatalf("schema violation error = %#v", err)
	}
}

func TestValidateChatRequestEnforcesBoundsBeforeToolArgumentDecoding(t *testing.T) {
	t.Parallel()

	twoToolCalls := func() []CanonicalToolCall {
		return []CanonicalToolCall{
			{ID: "a", Name: "f", Arguments: `{}`},
			{ID: "b", Name: "f", Arguments: `{}`},
		}
	}
	tests := []struct {
		name string
		edit func(*CanonicalChatRequest, *Limits)
		path string
	}{
		{"message bytes", func(request *CanonicalChatRequest, limits *Limits) {
			request.Messages = []CanonicalMessage{{Role: RoleAssistant, ToolCalls: twoToolCalls()}}
			limits.MaxMessageBytes = 7
		}, "messages[0]"},
		{"request content bytes", func(request *CanonicalChatRequest, limits *Limits) {
			request.Messages = []CanonicalMessage{{Role: RoleAssistant, ToolCalls: twoToolCalls()}}
			limits.MaxRequestContentBytes = 7
		}, "messages"},
		{"tool arguments bytes", func(request *CanonicalChatRequest, limits *Limits) {
			request.Messages = []CanonicalMessage{{Role: RoleAssistant, ToolCalls: []CanonicalToolCall{{ID: "a", Name: "f", Arguments: `{}`}}}}
			limits.MaxToolArgumentsBytes = 1
		}, "messages[0].tool_calls[0].arguments"},
		{"content parts", func(request *CanonicalChatRequest, limits *Limits) {
			request.Messages[0].Content = []CanonicalContentPart{{Type: ContentText, Text: "a"}, {Type: ContentText, Text: "b"}}
			limits.MaxContentParts = 1
		}, "messages[0].content"},
		{"tool calls", func(request *CanonicalChatRequest, limits *Limits) {
			request.Messages = []CanonicalMessage{{Role: RoleAssistant, ToolCalls: twoToolCalls()}}
			limits.MaxToolCallsPerMessage = 1
		}, "messages[0].tool_calls"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request := validRequest()
			limits := DefaultLimits()
			test.edit(&request, &limits)
			_, err := ValidateChatRequest(request, limits)
			if err == nil || err.Code != FailureClientInvalidRequest || err.RetryDisposition != RetryNever {
				t.Fatalf("ValidateChatRequest() error = %#v", err)
			}
			if err.Validation == nil || err.Validation.Path != test.path {
				t.Fatalf("validation = %#v, want path %q", err.Validation, test.path)
			}
		})
	}
}

func TestRouteCapabilitiesRejectUnverifiedRequirements(t *testing.T) {
	t.Parallel()

	route := RouteCapabilities{Claims: map[Capability]CapabilityClaim{
		CapabilityEndpointStreaming: {State: CapabilitySupported, FixtureVersion: ContractVersion},
		CapabilityToolsFunction:     {State: CapabilityUnverified},
	}}
	if err := route.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	want := []Capability{CapabilityToolsFunction, CapabilityToolsParallel}
	if got := route.Missing([]Capability{CapabilityToolsParallel, CapabilityEndpointStreaming, CapabilityToolsFunction}); !slices.Equal(got, want) {
		t.Fatalf("Missing() = %v, want %v", got, want)
	}
}

func FuzzValidateChatRequestNeverPanics(f *testing.F) {
	f.Add("hello", `{"type":"object"}`)
	f.Add("\xff", `{"required":[]}`)
	f.Fuzz(func(t *testing.T, content, rawSchema string) {
		request := validRequest()
		request.Messages[0].Content[0].Text = content
		request.Tools = []CanonicalFunctionTool{{Name: "tool", Parameters: NewJSONSchema([]byte(rawSchema))}}
		request.ToolChoice.Kind = ToolChoiceAuto
		_, _ = ValidateChatRequest(request, DefaultLimits())
	})
}

func validRequest() CanonicalChatRequest {
	return CanonicalChatRequest{
		ContractVersion: ContractVersion,
		RequestID:       "request-1",
		Target:          "group/default",
		Messages: []CanonicalMessage{{
			Role:    RoleUser,
			Content: []CanonicalContentPart{{Type: ContentText, Text: "hello"}},
		}},
		ToolChoice:     ToolChoice{Kind: ToolChoiceNone},
		ResponseFormat: ResponseFormat{Kind: ResponseFormatText},
		Deadline:       time.Unix(1_800_000_000, 0),
	}
}
