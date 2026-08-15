package auth

import (
	"testing"

	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/protocol"
)

func TestBindAttributionScopesIdentifiersToApplication(t *testing.T) {
	normalized := protocol.Attribution{
		ConversationID: protocol.Some("conversation-1"),
		RunID:          protocol.Some("run-1"),
	}
	first := BindAttribution(PrincipalContext{ApplicationID: "application-a"}, normalized)
	second := BindAttribution(PrincipalContext{ApplicationID: "application-b"}, normalized)

	firstConversation, present := first.ConversationID.Get()
	if !present || firstConversation.ApplicationID != "application-a" || firstConversation.Value != "conversation-1" {
		t.Fatalf("first conversation = %#v, %v", firstConversation, present)
	}
	secondConversation, present := second.ConversationID.Get()
	if !present || secondConversation.ApplicationID != "application-b" || secondConversation.Value != "conversation-1" {
		t.Fatalf("second conversation = %#v, %v", secondConversation, present)
	}
	if firstConversation == secondConversation {
		t.Fatal("the same client identifier collided across application namespaces")
	}
	firstRun, present := first.RunID.Get()
	if !present || firstRun.ApplicationID != "application-a" || firstRun.Value != "run-1" {
		t.Fatalf("first run = %#v, %v", firstRun, present)
	}
}

func TestBindAttributionPreservesAbsentValues(t *testing.T) {
	bound := BindAttribution(PrincipalContext{ApplicationID: "application-a"}, protocol.Attribution{})
	if bound.ConversationID.IsSet() || bound.RunID.IsSet() {
		t.Fatalf("BindAttribution() = %#v", bound)
	}
}
