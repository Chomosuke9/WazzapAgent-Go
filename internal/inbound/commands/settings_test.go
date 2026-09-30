package commands

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Chomosuke9/DiscordAgent-Go/internal/agent"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/command"
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
	for _, menu := range buttons.sent[0].Menus {
		for _, row := range menu.Rows {
			ids = append(ids, row.ID)
		}
	}
	return buttons.sent[0].Text, ids, nil
}

func TestSettingsShowsTheCurrentSettingsWithAMenuPerPart(t *testing.T) {
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
	want := "⚙️ **Chat settings**\n\nCurrent:\n- When I reply: mention, smart, 1 smart rule\n- Moderation: level 2 (delete + mute)\n" +
		"- Custom instructions: set, added to the main prompt\n- Tasks: 1 one-off, 1 daily; ask me to list or change them"
	if text != want {
		t.Fatalf("settings text:\n%s\nwant:\n%s", text, want)
	}
	wantIDs := []string{
		"/trigger mention off", "/trigger reply on", "/trigger name on", "/trigger smart off", "/trigger",
		"/permission 0", "/permission 1", "/permission 2", "/permission 3",
		"/prompt", "/reset",
	}
	if !slices.Equal(ids, wantIDs) {
		t.Fatalf("menu options = %q", ids)
	}

	text, ids, err = dispatchWithButtons(t, "/settings", command.PermissionFacts{IsPrivate: true, IsOwner: true}, agent.ConfigSnapshot{}, &recordingTasks{})
	if err != nil || !slices.Equal(ids, []string{"/prompt", "/reset"}) || strings.Contains(text, "When I reply") {
		t.Fatalf("direct chat = %q, %q, %v", text, ids, err)
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

func TestPromptSetsInstructionsAndShowsAppReplaceMode(t *testing.T) {
	if action, text := parsePromptArgs("set Speak like a pirate.", true); action != "set" || text != "Speak like a pirate." {
		t.Fatalf("set = %q %q", action, text)
	}
	if action, _ := parsePromptArgs("replace Speak like a pirate.", true); action != "" {
		t.Fatalf("replace = %q, want it unsupported", action)
	}
	// Replace mode can still be chosen in the app's chat panel.
	config := agent.ConfigSnapshot{PromptOverride: &agent.PromptOverride{Mode: agent.PromptReplace, Text: "Speak like a pirate."}}
	text, ids, err := dispatchWithButtons(t, "/prompt", command.PermissionFacts{IsGroup: true, IsAdmin: true}, config, nil)
	if err != nil || !strings.Contains(text, "used instead of the main prompt:\nSpeak like a pirate.") || !slices.Equal(ids, []string{"/prompt clear"}) {
		t.Fatalf("/prompt = %q %q %v", text, ids, err)
	}
}
