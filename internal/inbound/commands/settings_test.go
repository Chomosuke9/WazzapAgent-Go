package commands

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
)

// dispatchWithButtons runs text as a typed command and returns the one
// button message it sent.
func dispatchWithButtons(t *testing.T, text string, facts command.PermissionFacts, config agent.ConfigSnapshot, tasks command.TaskScheduler) (string, []string, error) {
	t.Helper()
	registry := builtinRegistry(t)
	request, _, recognized := registry.Parse(text)
	if !recognized {
		t.Fatalf("%s is not registered", text)
	}
	buttons := &recordingButtons{}
	err := registry.Dispatch(t.Context(), request, command.Invocation{
		Facts: facts, Config: config,
		Platform: command.Platform{Text: &recordingText{}, Buttons: buttons, Tasks: tasks},
	})
	if err != nil || len(buttons.sent) != 1 {
		return "", nil, errors.Join(err, errors.New("no button message"))
	}
	var ids []string
	for _, button := range buttons.sent[0].Buttons {
		ids = append(ids, button.ID)
	}
	return buttons.sent[0].Text, ids, nil
}

func TestSettingsShowsEverythingWithButtonsToEachCommand(t *testing.T) {
	zone := time.FixedZone("", 7*60*60)
	tasks := &recordingTasks{saved: []command.Task{
		{Code: "a1b2c3", Prompt: "remind about the meeting", FireAt: time.Date(2026, 9, 28, 20, 30, 0, 0, zone)},
		{Code: "d4e5f6", Prompt: "say good morning", FireAt: time.Date(2026, 9, 29, 7, 0, 0, 0, zone), Daily: true},
	}}
	config := agent.ConfigSnapshot{
		Triggers:       agent.TriggerConfig{Mention: true, Smart: true, SmartRules: "scam link: delete it"},
		Permission:     agent.PermissionConfig{ModerationLevel: 2},
		PromptOverride: &agent.PromptOverride{Mode: agent.PromptAppend, Text: "Keep replies short."},
	}
	text, ids, err := dispatchWithButtons(t, "/settings", command.PermissionFacts{IsGroup: true, IsAdmin: true}, config, tasks)
	if err != nil {
		t.Fatalf("dispatch /settings: %v", err)
	}
	for _, want := range []string{
		"✅ mention, ❌ reply, ❌ name, ✅ smart", "1 smart rule;", "level 2 of 3", "added to the main prompt: Keep replies short.",
		"• a1b2c3, 28 Sep 20:30: remind about the meeting", "• d4e5f6, every day at 07:00 (UTC+07:00): say good morning",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("settings text lacks %q:\n%s", want, text)
		}
	}
	if !slices.Equal(ids, []string{"/trigger", "/permission", "/prompt"}) {
		t.Fatalf("buttons = %q", ids)
	}

	_, ids, err = dispatchWithButtons(t, "/settings", command.PermissionFacts{IsPrivate: true, IsOwner: true}, agent.ConfigSnapshot{}, &recordingTasks{})
	if err != nil || !slices.Equal(ids, []string{"/prompt"}) {
		t.Fatalf("direct chat buttons = %q, %v", ids, err)
	}
	for _, facts := range []command.PermissionFacts{{IsGroup: true}, {IsGroup: true, IsAdmin: true, FromMe: true}} {
		if _, _, err := dispatchWithButtons(t, "/settings", facts, agent.ConfigSnapshot{}, &recordingTasks{}); !errors.Is(err, command.ErrDenied) {
			t.Errorf("/settings with %+v: err = %v, want denied", facts, err)
		}
	}
}

func TestPermissionOffersOneButtonPerLevel(t *testing.T) {
	config := agent.ConfigSnapshot{Permission: agent.PermissionConfig{ModerationLevel: 1}}
	text, ids, err := dispatchWithButtons(t, "/permission", command.PermissionFacts{IsGroup: true, IsAdmin: true}, config, nil)
	if err != nil || !strings.Contains(text, "level 1 of 3. I may delete messages.") ||
		!slices.Equal(ids, []string{"/permission 0", "/permission 1", "/permission 2", "/permission 3"}) {
		t.Fatalf("/permission = %q %q %v", text, ids, err)
	}
}

func TestPromptReplaceUsesReplaceMode(t *testing.T) {
	if action, text := parsePromptArgs("replace Speak like a pirate.", true); action != "replace" || text != "Speak like a pirate." {
		t.Fatalf("replace = %q %q", action, text)
	}
	if action, _ := parsePromptArgs("replace ", true); action != "" {
		t.Fatalf("empty replace = %q", action)
	}
	config := agent.ConfigSnapshot{PromptOverride: &agent.PromptOverride{Mode: agent.PromptReplace, Text: "Speak like a pirate."}}
	text, ids, err := dispatchWithButtons(t, "/prompt", command.PermissionFacts{IsGroup: true, IsAdmin: true}, config, nil)
	if err != nil || !strings.Contains(text, "used instead of the main prompt:\nSpeak like a pirate.") || !slices.Equal(ids, []string{"/prompt clear"}) {
		t.Fatalf("/prompt = %q %q %v", text, ids, err)
	}
}
