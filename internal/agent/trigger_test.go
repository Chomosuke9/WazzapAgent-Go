package agent

import (
	"strings"
	"testing"
)

func TestDefaultTriggerConfigPreservesMentionAndReplyBehavior(t *testing.T) {
	triggers := DefaultTriggerConfig()
	if !triggers.Matches(true, false, "hello", "Vivy") || !triggers.Matches(false, true, "hello", "Vivy") {
		t.Fatal("default triggers must allow mention and reply")
	}
	if triggers.Matches(false, false, "hello Vivy", "Vivy") {
		t.Fatal("the name trigger must be opt-in by default")
	}
}

func TestNameTriggerSupportsAssistantNameAndCustomRegex(t *testing.T) {
	triggers := TriggerConfig{Name: true}
	if !triggers.Matches(false, false, "hey VIVY, help", "Vivy") {
		t.Fatal("standard name trigger should match case-insensitively")
	}
	triggers.NameRegex = true
	triggers.NamePattern = `(?i)\bvivy\b.*help`
	if err := triggers.Validate(); err != nil {
		t.Fatalf("validate regex trigger: %v", err)
	}
	if !triggers.Matches(false, false, "Vivy, please help", "other name") || triggers.Matches(false, false, "Vivy, hello", "Vivy") {
		t.Fatal("custom regex should replace the assistant-name matcher")
	}
}

func TestTriggerConfigRejectsInvalidRegexAndOversizedPattern(t *testing.T) {
	for _, pattern := range []string{"[", strings.Repeat("x", MaxTriggerPatternBytes+1)} {
		if err := (TriggerConfig{NameRegex: true, NamePattern: pattern}).Validate(); err == nil {
			t.Fatalf("accepted invalid trigger pattern %q", pattern)
		}
	}
}
