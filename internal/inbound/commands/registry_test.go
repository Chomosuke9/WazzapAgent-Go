package commands_test

import (
	"testing"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
	builtincommands "github.com/Chomosuke9/WazzapAgent-Go/internal/inbound/commands"
)

func TestGeneratedRegistryContainsAllBuiltInCommands(t *testing.T) {
	registry, err := command.NewRegistry(builtincommands.Descriptors)
	if err != nil {
		t.Fatalf("create generated registry: %v", err)
	}
	want := map[string]string{
		"/help":        "help",
		"/menu":        "help",
		"/info":        "info",
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
