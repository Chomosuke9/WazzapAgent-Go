package commands

import (
	"slices"
	"strings"
	"testing"

	"github.com/Chomosuke9/DiscordAgent-Go/internal/agent"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/command"
)

func TestEveryCommandFileRegistersItself(t *testing.T) {
	registry := builtinRegistry(t)
	want := map[string]string{
		"/help":        "help",
		"/menu":        "help",
		"/info":        "info",
		"/mod":         "mod",
		"/dump":        "dump",
		"/catch":       "catch",
		"/reset":       "reset",
		"/prompt":      "prompt",
		"/permission":  "permission",
		"/permissions": "permission",
		"/trigger":     "trigger",
	}
	for text, name := range want {
		request, cmd, recognized := registry.Parse(text)
		if !recognized || request.Name != name || cmd.Run == nil {
			t.Fatalf("parse %q = %#v, %v", text, request, recognized)
		}
	}
}

func TestCommandFilesOwnTheirArgumentGrammar(t *testing.T) {
	promptTests := []struct {
		args, action string
		hasArgs      bool
	}{
		{args: "", hasArgs: false, action: "view"},
		{args: "view", hasArgs: true, action: "view"},
		{args: "", hasArgs: true, action: ""},
		{args: "clear", hasArgs: true, action: "clear"},
		{args: "set hello", hasArgs: true, action: "set"},
		{args: "set " + strings.Repeat("x", agent.MaxPromptBytes+1), hasArgs: true, action: ""},
		{args: "delete", hasArgs: true, action: ""},
	}
	for _, test := range promptTests {
		if got, _ := parsePromptArgs(test.args, test.hasArgs); got != test.action {
			t.Fatalf("prompt %q action = %q, want %q", test.args, got, test.action)
		}
	}
	permissionTests := []struct {
		args        string
		hasArgs     bool
		set, ok     bool
		wantedLevel agent.ModerationLevel
	}{
		{args: "", hasArgs: false, ok: true},
		{args: "view", hasArgs: true, ok: true},
		{args: "", hasArgs: true, ok: false},
		{args: "2", hasArgs: true, set: true, ok: true, wantedLevel: 2},
		{args: "4", hasArgs: true, ok: false},
	}
	for _, test := range permissionTests {
		level, set, ok := parsePermissionArgs(test.args, test.hasArgs)
		if set != test.set || ok != test.ok || level != test.wantedLevel {
			t.Fatalf("permission %q = %v/%v/%v", test.args, level, set, ok)
		}
	}
	triggerTests := []struct {
		args string
		ok   bool
	}{
		{args: "mention on", ok: true},
		{args: "name off", ok: true},
		{args: "reply on", ok: true},
		{args: "regex off", ok: true},
		{args: `pattern (?i)\bvivy\b`, ok: true},
		{args: "name maybe", ok: false},
		{args: "pattern ", ok: false},
		{args: "mention", ok: false},
		{args: "smart on", ok: true},
		{args: "smart clear", ok: true},
		{args: "smart set ", ok: false},
		{args: "smart settings", ok: false},
		{args: "smart add scam links", ok: true},
		{args: "smart add ", ok: false},
		{args: "smart remove 2", ok: true},
		{args: "smart remove 0", ok: false},
		{args: "smart remove two", ok: false},
	}
	for _, test := range triggerTests {
		if _, _, ok := parseTriggerArgs(test.args); ok != test.ok {
			t.Fatalf("trigger %q ok = %v, want %v", test.args, ok, test.ok)
		}
	}
	change, _, ok := parseTriggerArgs("smart set\nsomeone sends a scam link\nsomeone asks about prices")
	if !ok {
		t.Fatal("multi-line smart rules were rejected")
	}
	triggers := agent.TriggerConfig{}
	change(&triggers)
	if !triggers.Smart || triggers.SmartRules != "someone sends a scam link\nsomeone asks about prices" {
		t.Fatalf("smart set = %#v", triggers)
	}
	add, _, _ := parseTriggerArgs("smart add someone spams\n stickers")
	add(&triggers)
	if triggers.SmartRules != "someone sends a scam link\nsomeone asks about prices\nsomeone spams stickers" {
		t.Fatalf("smart add = %q", triggers.SmartRules)
	}
	remove, _, _ := parseTriggerArgs("smart remove 2")
	remove(&triggers)
	if triggers.SmartRules != "someone sends a scam link\nsomeone spams stickers" {
		t.Fatalf("smart remove 2 = %q", triggers.SmartRules)
	}
	removeMissing, _, _ := parseTriggerArgs("smart remove 5")
	removeMissing(&triggers)
	if triggers.SmartRules != "someone sends a scam link\nsomeone spams stickers" {
		t.Fatalf("smart remove 5 changed the rules: %q", triggers.SmartRules)
	}
	clear, _, _ := parseTriggerArgs("smart clear")
	clear(&triggers)
	if !triggers.Smart || triggers.SmartRules != "" {
		t.Fatalf("smart clear = %#v", triggers)
	}
}

func TestTriggerPermissionAllowsOwnerGroupAdminOrTheBotInGroups(t *testing.T) {
	_, trigger, _ := builtinRegistry(t).Parse("/trigger")
	tests := []struct {
		name  string
		facts command.PermissionFacts
		want  bool
	}{
		{name: "owner in private chat", facts: command.PermissionFacts{IsOwner: true, IsPrivate: true}, want: false},
		{name: "owner in group", facts: command.PermissionFacts{IsOwner: true, IsGroup: true}, want: true},
		{name: "group admin", facts: command.PermissionFacts{IsGroup: true, IsAdmin: true}, want: true},
		{name: "admin in private chat", facts: command.PermissionFacts{IsAdmin: true, IsPrivate: true}, want: false},
		{name: "owner without group fact", facts: command.PermissionFacts{IsOwner: true}, want: false},
		{name: "ordinary group member", facts: command.PermissionFacts{IsGroup: true}, want: false},
		{name: "bot in group", facts: command.PermissionFacts{IsGroup: true, FromMe: true}, want: true},
		{name: "bot in private chat", facts: command.PermissionFacts{IsPrivate: true, FromMe: true}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := command.EvaluatePermission(trigger.Permission, test.facts)
			if err != nil || got != test.want {
				t.Fatalf("permission result = %v, err=%v; want %v", got, err, test.want)
			}
		})
	}
}

func TestTriggerViewIsPlainTextWithUsage(t *testing.T) {
	registry := builtinRegistry(t)
	text, buttons := &recordingText{}, &recordingButtons{}
	request, _, _ := registry.Parse("/trigger")
	err := registry.Dispatch(t.Context(), request, command.Invocation{
		Facts: command.PermissionFacts{IsGroup: true, IsAdmin: true}, Platform: command.Platform{Text: text, Buttons: buttons},
		Config: agent.ConfigSnapshot{Triggers: agent.TriggerConfig{Mention: true, SmartRules: "someone sends a scam link: delete it"}},
	})
	if err != nil || len(buttons.sent) != 0 || len(text.sent) != 1 {
		t.Fatalf("/trigger = %v, %d button messages, texts %q", err, len(buttons.sent), text.sent)
	}
	if view := text.sent[0]; !strings.Contains(view, "✅ Mention") || !strings.Contains(view, "1. someone sends a scam link: delete it") ||
		!strings.Contains(view, "/trigger toggle: buttons to turn each one on or off") || !strings.Contains(view, "/trigger smart add <rule>") {
		t.Fatalf("view = %q", view)
	}
}

func TestTriggerToggleShowsRulesWithButtonsThatRouteBackToTrigger(t *testing.T) {
	registry := builtinRegistry(t)
	facts := command.PermissionFacts{IsGroup: true, IsAdmin: true}
	buttons := &recordingButtons{}
	request, _, _ := registry.Parse("/trigger toggle")
	err := registry.Dispatch(t.Context(), request, command.Invocation{
		Facts: facts, Platform: command.Platform{Buttons: buttons},
		Config: agent.ConfigSnapshot{Triggers: agent.TriggerConfig{Mention: true, SmartRules: "someone sends a scam link: delete it\nsomeone asks for prices"}},
	})
	if err != nil {
		t.Fatalf("dispatch /trigger toggle: %v", err)
	}
	if len(buttons.sent) != 1 {
		t.Fatalf("button messages = %d, want 1", len(buttons.sent))
	}
	text := buttons.sent[0].Text
	if !strings.Contains(text, "✅ Mention") || !strings.Contains(text, "❌ Smart") ||
		!strings.Contains(text, "paused while Smart is off") || !strings.Contains(text, "1. someone sends a scam link: delete it\n2. someone asks for prices") {
		t.Fatalf("view = %q", text)
	}
	var ids []string
	for _, button := range buttons.sent[0].Buttons {
		ids = append(ids, button.ID)
	}
	want := []string{"/trigger mention off", "/trigger reply on", "/trigger name on", "/trigger smart on", "/trigger smart remove 1", "/trigger smart remove 2"}
	if !slices.Equal(ids, want) {
		t.Fatalf("buttons = %q, want %q", ids, want)
	}
	for _, id := range ids {
		if request, _, recognized := registry.Parse(id); !recognized || request.Name != "trigger" {
			t.Fatalf("tap %q does not route back to /trigger", id)
		}
		if _, _, ok := parseTriggerArgs(strings.TrimPrefix(id, "/trigger ")); !ok {
			t.Fatalf("tap %q is not valid /trigger syntax", id)
		}
	}
}
