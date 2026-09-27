package app

import (
	"strings"
	"testing"
)

func TestRenderSystemPolicyRendersNameAndLeavesDateForEachRequest(t *testing.T) {
	rendered := RenderSystemPolicy("Wazzap")
	for _, value := range []string{"The assistant is Wazzap", "Today's date: {{current_date}}", "@Wazzap (Bot)"} {
		if !strings.Contains(rendered, value) {
			t.Fatalf("rendered policy lacks %q", value)
		}
	}
	if strings.Contains(rendered, "{{assistant_name}}") {
		t.Fatal("assistant name placeholder was not rendered")
	}
	if strings.Contains(rendered, "@Bot (Bot)") {
		t.Fatal("rendered policy still contains the old bot mention")
	}
}

func TestRenderSystemPolicyPreservesUnknownBraces(t *testing.T) {
	const unknown = "{{literal_example}}"
	rendered := renderSystemPolicy("name={{assistant_name}} literal="+unknown, "Wazzap")
	if !strings.Contains(rendered, unknown) {
		t.Fatalf("unknown placeholder %q was changed", unknown)
	}
}
