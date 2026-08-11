package protocol

// Limits bounds all request, response, schema, and incremental assembly work
// performed by this package. A zero-value Limits is invalid; use DefaultLimits.
type Limits struct {
	MaxIdentifierBytes      int
	MaxTargetBytes          int
	MaxMessages             int
	MaxContentParts         int
	MaxMessageBytes         int
	MaxRequestContentBytes  int
	MaxParticipantNameBytes int
	MaxTools                int
	MaxToolNameBytes        int
	MaxToolDescriptionBytes int
	MaxToolCallsPerMessage  int
	MaxToolArgumentsBytes   int
	MaxSchemaBytes          int
	MaxSchemaDepth          int
	MaxStopSequences        int
	MaxStopSequenceBytes    int
	MaxOutputTokens         int
	MaxEventTextBytes       int
	MaxResponseTextBytes    int
	MaxSafeErrorBytes       int
}

// DefaultLimits returns conservative in-process protocol bounds. Route-specific
// limits may be lower but must never be represented by silently truncating data.
func DefaultLimits() Limits {
	return Limits{
		MaxIdentifierBytes:      128,
		MaxTargetBytes:          256,
		MaxMessages:             128,
		MaxContentParts:         256,
		MaxMessageBytes:         1 << 20,
		MaxRequestContentBytes:  4 << 20,
		MaxParticipantNameBytes: 64,
		MaxTools:                128,
		MaxToolNameBytes:        64,
		MaxToolDescriptionBytes: 8 << 10,
		MaxToolCallsPerMessage:  128,
		MaxToolArgumentsBytes:   1 << 20,
		MaxSchemaBytes:          128 << 10,
		MaxSchemaDepth:          32,
		MaxStopSequences:        4,
		MaxStopSequenceBytes:    1024,
		MaxOutputTokens:         1_000_000,
		MaxEventTextBytes:       1 << 20,
		MaxResponseTextBytes:    4 << 20,
		MaxSafeErrorBytes:       1024,
	}
}

func (l Limits) validate() *CanonicalError {
	values := []struct {
		path  string
		value int
	}{
		{"limits.max_identifier_bytes", l.MaxIdentifierBytes},
		{"limits.max_target_bytes", l.MaxTargetBytes},
		{"limits.max_messages", l.MaxMessages},
		{"limits.max_content_parts", l.MaxContentParts},
		{"limits.max_message_bytes", l.MaxMessageBytes},
		{"limits.max_request_content_bytes", l.MaxRequestContentBytes},
		{"limits.max_participant_name_bytes", l.MaxParticipantNameBytes},
		{"limits.max_tools", l.MaxTools},
		{"limits.max_tool_name_bytes", l.MaxToolNameBytes},
		{"limits.max_tool_description_bytes", l.MaxToolDescriptionBytes},
		{"limits.max_tool_calls_per_message", l.MaxToolCallsPerMessage},
		{"limits.max_tool_arguments_bytes", l.MaxToolArgumentsBytes},
		{"limits.max_schema_bytes", l.MaxSchemaBytes},
		{"limits.max_schema_depth", l.MaxSchemaDepth},
		{"limits.max_stop_sequences", l.MaxStopSequences},
		{"limits.max_stop_sequence_bytes", l.MaxStopSequenceBytes},
		{"limits.max_output_tokens", l.MaxOutputTokens},
		{"limits.max_event_text_bytes", l.MaxEventTextBytes},
		{"limits.max_response_text_bytes", l.MaxResponseTextBytes},
		{"limits.max_safe_error_bytes", l.MaxSafeErrorBytes},
	}
	for _, item := range values {
		if item.value <= 0 {
			return invalidRequest(item.path, "must be positive")
		}
	}
	return nil
}
