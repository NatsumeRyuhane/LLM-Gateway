package protocol

import "slices"

// Capability is a closed gateway.adapter.v0 semantic understood by routing.
type Capability string

const (
	CapabilityEndpointBuffered          Capability = "endpoint.chat_completions.buffered"
	CapabilityEndpointStreaming         Capability = "endpoint.chat_completions.streaming"
	CapabilityRoleDeveloper             Capability = "message.roles.developer"
	CapabilityRoleSystem                Capability = "message.roles.system"
	CapabilityRoleUser                  Capability = "message.roles.user"
	CapabilityRoleAssistant             Capability = "message.roles.assistant"
	CapabilityRoleTool                  Capability = "message.roles.tool"
	CapabilityParticipantName           Capability = "message.participant_name"
	CapabilityRefusalOutput             Capability = "message.refusal_output"
	CapabilityContentText               Capability = "content.text"
	CapabilityToolsFunction             Capability = "tools.function"
	CapabilityToolsFunctionSchemaStrict Capability = "tools.function_schema_strict"
	CapabilityToolsChoiceNone           Capability = "tools.choice.none"
	CapabilityToolsChoiceAuto           Capability = "tools.choice.auto"
	CapabilityToolsChoiceRequired       Capability = "tools.choice.required"
	CapabilityToolsChoiceSpecific       Capability = "tools.choice.specific"
	CapabilityToolsParallel             Capability = "tools.parallel"
	CapabilityStructuredJSONObject      Capability = "structured.json_object"
	CapabilityStructuredJSONSchema      Capability = "structured.json_schema"
	CapabilityStructuredJSONStrict      Capability = "structured.json_schema_strict"
	CapabilityStructuredStreaming       Capability = "structured.streaming"
	CapabilityParameterTemperature      Capability = "parameter.temperature"
	CapabilityParameterTopP             Capability = "parameter.top_p"
	CapabilityParameterSeed             Capability = "parameter.seed"
	CapabilityParameterStop             Capability = "parameter.stop"
	CapabilityParameterMaxOutputTokens  Capability = "parameter.max_output_tokens"
	CapabilityUsageBuffered             Capability = "usage.buffered"
	CapabilityUsageStreaming            Capability = "usage.streaming"
	CapabilityUsageCacheDetails         Capability = "usage.cache_details"
	CapabilityUsageReasoningDetails     Capability = "usage.reasoning_details"
	CapabilityFinishStop                Capability = "finish_reason.stop"
	CapabilityFinishLength              Capability = "finish_reason.length"
	CapabilityFinishToolCalls           Capability = "finish_reason.tool_calls"
	CapabilityFinishContentFilter       Capability = "finish_reason.content_filter"
)

var knownCapabilities = map[Capability]struct{}{
	CapabilityEndpointBuffered: {}, CapabilityEndpointStreaming: {},
	CapabilityRoleDeveloper: {}, CapabilityRoleSystem: {}, CapabilityRoleUser: {}, CapabilityRoleAssistant: {}, CapabilityRoleTool: {},
	CapabilityParticipantName: {}, CapabilityRefusalOutput: {}, CapabilityContentText: {},
	CapabilityToolsFunction: {}, CapabilityToolsFunctionSchemaStrict: {}, CapabilityToolsChoiceNone: {}, CapabilityToolsChoiceAuto: {}, CapabilityToolsChoiceRequired: {}, CapabilityToolsChoiceSpecific: {}, CapabilityToolsParallel: {},
	CapabilityStructuredJSONObject: {}, CapabilityStructuredJSONSchema: {}, CapabilityStructuredJSONStrict: {}, CapabilityStructuredStreaming: {},
	CapabilityParameterTemperature: {}, CapabilityParameterTopP: {}, CapabilityParameterSeed: {}, CapabilityParameterStop: {}, CapabilityParameterMaxOutputTokens: {},
	CapabilityUsageBuffered: {}, CapabilityUsageStreaming: {}, CapabilityUsageCacheDetails: {}, CapabilityUsageReasoningDetails: {},
	CapabilityFinishStop: {}, CapabilityFinishLength: {}, CapabilityFinishToolCalls: {}, CapabilityFinishContentFilter: {},
}

type capabilitySet map[Capability]struct{}

func (s capabilitySet) add(capability Capability) { s[capability] = struct{}{} }

func (s capabilitySet) sorted() []Capability {
	result := make([]Capability, 0, len(s))
	for capability := range s {
		result = append(result, capability)
	}
	slices.Sort(result)
	return result
}

type CapabilityState string

const (
	CapabilitySupported   CapabilityState = "supported"
	CapabilityUnsupported CapabilityState = "unsupported"
	CapabilityUnverified  CapabilityState = "unverified"
)

type CapabilityClaim struct {
	State          CapabilityState
	FixtureVersion string
}

// RouteCapabilities is keyed only by closed Capability constants. Validation
// rejects invented identifiers and unsupported claim states.
type RouteCapabilities struct {
	Claims map[Capability]CapabilityClaim
}

func (c RouteCapabilities) Validate() *CanonicalError {
	for capability, claim := range c.Claims {
		if _, ok := knownCapabilities[capability]; !ok {
			return invalidRequest("capabilities", "contains an unknown capability")
		}
		switch claim.State {
		case CapabilitySupported:
			if claim.FixtureVersion == "" {
				return invalidRequest("capabilities", "supported claims require conformance evidence")
			}
		case CapabilityUnsupported, CapabilityUnverified:
		default:
			return invalidRequest("capabilities", "contains an invalid capability state")
		}
	}
	return nil
}

// Missing returns required capabilities that are absent, unsupported, or
// unverified. The result is sorted for deterministic routing evidence.
func (c RouteCapabilities) Missing(required []Capability) []Capability {
	missing := make([]Capability, 0)
	for _, capability := range required {
		claim, ok := c.Claims[capability]
		if !ok || claim.State != CapabilitySupported {
			missing = append(missing, capability)
		}
	}
	slices.Sort(missing)
	return missing
}
