package openai

import (
	"strings"

	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/protocol"
)

func translateRequest(request protocol.ValidatedChatRequest, model string) chatRequest {
	canonical := request.Canonical()
	wired := chatRequest{Model: model, Stream: canonical.Stream}
	for _, input := range canonical.Messages {
		content := joinContent(input.Content)
		output := message{Role: string(input.Role)}
		if input.Name.IsSet() {
			value, _ := input.Name.Get()
			output.Name = &value
		}
		if content != "" {
			output.Content = &content
		}
		if input.ToolCallID.IsSet() {
			value, _ := input.ToolCallID.Get()
			output.ToolCallID = &value
		}
		for _, call := range input.ToolCalls {
			output.ToolCalls = append(output.ToolCalls, toolCall{ID: call.ID, Type: "function", Function: functionCall{Name: call.Name, Arguments: call.Arguments}})
		}
		wired.Messages = append(wired.Messages, output)
	}
	for _, input := range canonical.Tools {
		function := function{Name: input.Name, Parameters: input.Parameters.Bytes()}
		if value, ok := input.Description.Get(); ok {
			function.Description = &value
		}
		if value, ok := input.Strict.Get(); ok {
			function.Strict = &value
		}
		wired.Tools = append(wired.Tools, tool{Type: "function", Function: function})
	}
	if len(canonical.Tools) > 0 {
		switch canonical.ToolChoice.Kind {
		case protocol.ToolChoiceSpecific:
			wired.ToolChoice = map[string]any{"type": "function", "function": map[string]string{"name": canonical.ToolChoice.FunctionName}}
		default:
			wired.ToolChoice = string(canonical.ToolChoice.Kind)
		}
	}
	if value, ok := canonical.ParallelToolCalls.Get(); ok {
		wired.ParallelToolCalls = &value
	}
	switch canonical.ResponseFormat.Kind {
	case protocol.ResponseFormatJSONObject:
		wired.ResponseFormat = &responseFormat{Type: "json_object"}
	case protocol.ResponseFormatJSONSchema:
		name := "gateway_response"
		schema := &jsonSchema{Name: name, Schema: canonical.ResponseFormat.Schema.Bytes()}
		if strict, explicit := canonical.ResponseFormat.Strict.Get(); explicit {
			schema.Strict = &strict
		}
		wired.ResponseFormat = &responseFormat{Type: "json_schema", JSONSchema: schema}
	}
	if value, ok := canonical.Sampling.Temperature.Get(); ok {
		wired.Temperature = &value
	}
	if value, ok := canonical.Sampling.TopP.Get(); ok {
		wired.TopP = &value
	}
	if value, ok := canonical.Sampling.Seed.Get(); ok {
		wired.Seed = &value
	}
	if value, ok := canonical.Sampling.Stop.Get(); ok {
		wired.Stop = value
	}
	if value, ok := canonical.MaxOutputTokens.Get(); ok {
		wired.MaxTokens = &value
	}
	if canonical.Stream && canonical.IncludeUsage {
		wired.StreamOptions = &streamOptions{IncludeUsage: true}
	}
	return wired
}

func translateMessage(input message) protocol.CanonicalMessage {
	output := protocol.CanonicalMessage{Role: protocol.MessageRole(input.Role)}
	if input.Content != nil && *input.Content != "" {
		output.Content = []protocol.CanonicalContentPart{{Type: protocol.ContentText, Text: *input.Content}}
	}
	if input.Refusal != nil {
		output.Refusal = protocol.Some(*input.Refusal)
	}
	for _, call := range input.ToolCalls {
		output.ToolCalls = append(output.ToolCalls, protocol.CanonicalToolCall{ID: call.ID, Name: call.Function.Name, Arguments: call.Function.Arguments})
	}
	return output
}

func translateUsage(input usage, partial bool) protocol.CanonicalUsage {
	output := protocol.CanonicalUsage{
		InputTokens: input.PromptTokens, OutputTokens: input.CompletionTokens, TotalTokens: input.TotalTokens,
		Provenance: protocol.UsageProviderReported, Partial: partial,
	}
	if input.PromptTokensDetails != nil && input.PromptTokensDetails.CachedTokens != nil {
		output.CachedTokens = protocol.Some(*input.PromptTokensDetails.CachedTokens)
	}
	if input.CompletionTokensDetails != nil && input.CompletionTokensDetails.ReasoningTokens != nil {
		output.ReasoningTokens = protocol.Some(*input.CompletionTokensDetails.ReasoningTokens)
	}
	return output
}

func finishReason(value string) protocol.FinishReason { return protocol.FinishReason(value) }

func joinContent(parts []protocol.CanonicalContentPart) string {
	var output strings.Builder
	for _, part := range parts {
		output.WriteString(part.Text)
	}
	return output.String()
}
