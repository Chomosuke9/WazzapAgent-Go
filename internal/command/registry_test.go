package command_test

import (
	"testing"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/policy"
)

func TestRegistryCanonicalizesAliasesAndKeepsMalformedArgumentsRecognized(t *testing.T) {
	registry, err := command.NewRegistry([]command.Descriptor{
		{Name: "prompt", Aliases: []string{"prompts"}, Capability: policy.CapabilityPromptWrite},
		{Name: "help", Aliases: []string{"menu"}, Capability: policy.CapabilityCommandHelp},
	})
	if err != nil {
		t.Fatalf("create registry: %v", err)
	}
	request, descriptor, recognized := registry.Parse("/PROMPTS set x")
	if !recognized || request.Name != "prompt" || request.Arguments != "set x" || !request.ArgumentsPresent || descriptor.Capability != policy.CapabilityPromptWrite {
		t.Fatalf("parsed command = %#v, %#v, %v", request, descriptor, recognized)
	}
	request, _, recognized = registry.Parse("/prompt ")
	if !recognized || !request.ArgumentsPresent || request.Arguments != "" {
		t.Fatalf("trailing command separator was lost: %#v, %v", request, recognized)
	}
	if _, _, recognized := registry.Parse("/prompt unknown syntax"); !recognized {
		t.Fatal("known command with invalid arguments fell through")
	}
	if _, _, recognized := registry.Parse("/unregistered"); recognized {
		t.Fatal("unknown command was recognized")
	}
}

func TestRegistryRejectsAliasCollision(t *testing.T) {
	_, err := command.NewRegistry([]command.Descriptor{
		{Name: "help", Aliases: []string{"menu"}, Capability: policy.CapabilityCommandHelp},
		{Name: "info", Aliases: []string{"menu"}, Capability: policy.CapabilityCommandInfo},
	})
	if err == nil {
		t.Fatal("alias collision was accepted")
	}
}
