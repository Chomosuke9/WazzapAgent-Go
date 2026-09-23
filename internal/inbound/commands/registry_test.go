package commands

import (
	"strings"
	"testing"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
)

func TestGeneratedRegistryContainsAllBuiltInCommands(t *testing.T) {
	registry, err := command.NewRegistry(Descriptors)
	if err != nil {
		t.Fatalf("create generated registry: %v", err)
	}
	want := map[string]string{
		"/help":        "help",
		"/menu":        "help",
		"/info":        "info",
		"/group":       "group",
		"/dump":        "dump",
		"/reset":       "reset",
		"/prompt":      "prompt",
		"/permission":  "permission",
		"/permissions": "permission",
		"/trigger":     "trigger",
	}
	for text, name := range want {
		request, descriptor, recognized := registry.Parse(text)
		if !recognized || string(request.Name) != name || descriptor.Handler == nil {
			t.Fatalf("generated registry parse %q = %#v, %#v, %v", text, request, descriptor, recognized)
		}
	}
}

func TestCommandModulesOwnTheirArgumentGrammar(t *testing.T) {
	promptTests := []struct {
		raw  string
		kind command.PromptCommandKind
	}{
		{raw: "/prompt", kind: command.PromptView},
		{raw: "/prompt view", kind: command.PromptView},
		{raw: "/prompt ", kind: command.PromptInvalid},
		{raw: "/prompt clear", kind: command.PromptClear},
		{raw: "/prompt set hello", kind: command.PromptSet},
		{raw: "/prompt set " + strings.Repeat("x", agent.MaxPromptBytes+1), kind: command.PromptInvalid},
		{raw: "/prompt delete", kind: command.PromptInvalid},
	}
	for _, test := range promptTests {
		if got := parsePromptCommand(test.raw); got.Kind != test.kind {
			t.Fatalf("prompt %q kind = %v, want %v", test.raw, got.Kind, test.kind)
		}
	}
	permissionTests := []struct {
		raw  string
		kind command.PermissionCommandKind
	}{
		{raw: "/permission", kind: command.PermissionView},
		{raw: "/permission view", kind: command.PermissionView},
		{raw: "/permission ", kind: command.PermissionInvalid},
		{raw: "/permissions 2", kind: command.PermissionSet},
		{raw: "/permission 4", kind: command.PermissionInvalid},
	}
	for _, test := range permissionTests {
		if got := parsePermissionCommand(test.raw); got.Kind != test.kind {
			t.Fatalf("permission %q kind = %v, want %v", test.raw, got.Kind, test.kind)
		}
	}
	triggerTests := []struct {
		raw  string
		kind command.TriggerCommandKind
	}{
		{raw: "/trigger", kind: command.TriggerView},
		{raw: "/trigger view", kind: command.TriggerView},
		{raw: "/trigger mention on", kind: command.TriggerSetMention},
		{raw: "/trigger name off", kind: command.TriggerSetName},
		{raw: "/trigger reply on", kind: command.TriggerSetReply},
		{raw: "/trigger regex off", kind: command.TriggerSetRegex},
		{raw: "/trigger pattern (?i)\\bvivy\\b", kind: command.TriggerSetPattern},
		{raw: "/trigger name maybe", kind: command.TriggerInvalid},
		{raw: "/trigger pattern ", kind: command.TriggerInvalid},
	}
	for _, test := range triggerTests {
		if got := parseTriggerCommand(test.raw); got.Kind != test.kind {
			t.Fatalf("trigger %q kind = %v, want %v", test.raw, got.Kind, test.kind)
		}
	}
}

func TestTriggerPermissionAllowsOwnerOrGroupAdminButNeverBot(t *testing.T) {
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
			got, err := command.EvaluatePermission(TriggerCommand.Permission, test.facts)
			if err != nil || got != test.want {
				t.Fatalf("permission result = %v, err=%v; want %v", got, err, test.want)
			}
		})
	}
}
