package main

import (
	"strings"
	"testing"
	"time"
)

func TestEmbeddedSystemPromptRendersSupportedPlaceholders(t *testing.T) {
	rendered := renderSystemPrompt(embeddedSystemPrompt, "Wazzap", time.Date(2026, 9, 15, 1, 2, 3, 0, time.UTC))
	for _, value := range []string{"The assistant is Wazzap", "Today's date: 15 Sep 2026"} {
		if !strings.Contains(rendered, value) {
			t.Fatalf("rendered prompt lacks %q", value)
		}
	}
	for _, token := range []string{"{{assistant_name}}", "{{current_date}}"} {
		if strings.Contains(rendered, token) {
			t.Fatalf("placeholder %q was not rendered", token)
		}
	}
}

func TestRunRejectsInvalidConfiguration(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("WAZZAP_ENV_FILE", "")
	t.Setenv("WAZZAP_HTTP_ADDRESS", "invalid-address")

	if code := run(); code != 2 {
		t.Fatalf("run exit code = %d, want 2", code)
	}
}
