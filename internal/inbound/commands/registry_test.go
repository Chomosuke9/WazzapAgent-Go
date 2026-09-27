package commands

import (
	"strings"
	"testing"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
)

func TestEveryCommandFileRegistersItself(t *testing.T) {
	registry := builtinRegistry(t)
	want := map[string]string{
		"/help":        "help",
		"/menu":        "help",
		"/info":        "info",
		"/group":       "group",
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
	}
	for _, test := range triggerTests {
		if _, ok := parseTriggerArgs(test.args); ok != test.ok {
			t.Fatalf("trigger %q ok = %v, want %v", test.args, ok, test.ok)
		}
	}
}

func TestTriggerPermissionAllowsOwnerOrGroupAdminButNeverBot(t *testing.T) {
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
		{name: "bot group admin", facts: command.PermissionFacts{IsGroup: true, IsAdmin: true, FromMe: true}, want: false},
		{name: "bot owner", facts: command.PermissionFacts{IsOwner: true, FromMe: true}, want: false},
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

func TestTriggerViewOffersToggleButtonsThatRouteBackToTrigger(t *testing.T) {
	registry := builtinRegistry(t)
	facts := command.PermissionFacts{IsGroup: true, IsAdmin: true}
	buttons := &recordingButtons{}
	request, _, _ := registry.Parse("/trigger")
	err := registry.Dispatch(t.Context(), request, command.Invocation{
		Facts: facts, Platform: command.Platform{Buttons: buttons},
		Config: agent.ConfigSnapshot{Triggers: agent.TriggerConfig{Mention: true}},
	})
	if err != nil {
		t.Fatalf("dispatch /trigger: %v", err)
	}
	if len(buttons.sent) != 1 {
		t.Fatalf("button messages = %d, want 1", len(buttons.sent))
	}
	got := buttons.sent[0].Buttons
	if len(got) != 3 || got[0].ID != "/trigger mention off" || got[1].ID != "/trigger name on" || got[2].ID != "/trigger reply on" {
		t.Fatalf("buttons = %#v", got)
	}
	for _, button := range got {
		if request, _, recognized := registry.Parse(button.ID); !recognized || request.Name != "trigger" {
			t.Fatalf("tap %q does not route back to /trigger", button.ID)
		}
		if _, ok := parseTriggerArgs(strings.TrimPrefix(button.ID, "/trigger ")); !ok {
			t.Fatalf("tap %q is not valid /trigger syntax", button.ID)
		}
	}
}
