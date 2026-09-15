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
}
