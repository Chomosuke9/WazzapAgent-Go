package command_test

import (
	"context"
	"testing"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/policy"
)

func TestRegistryCanonicalizesAliasesAndKeepsMalformedArgumentsRecognized(t *testing.T) {
	registry, err := command.NewRegistry([]command.Descriptor{
		{Name: "prompt", Aliases: []string{"prompts"}, Capability: policy.CapabilityPromptWrite, Permission: "owner"},
		{Name: "help", Aliases: []string{"menu"}, Capability: policy.CapabilityCommandHelp, Permission: "public"},
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
		{Name: "help", Aliases: []string{"menu"}, Capability: policy.CapabilityCommandHelp, Permission: "public"},
		{Name: "info", Aliases: []string{"menu"}, Capability: policy.CapabilityCommandInfo, Permission: "public"},
	})
	if err == nil {
		t.Fatal("alias collision was accepted")
	}
}

func TestRegistryDispatchesCanonicalAndAliasRequests(t *testing.T) {
	called := false
	registry, err := command.NewRegistry([]command.Descriptor{
		{
			Name:       "help",
			Aliases:    []string{"menu"},
			Capability: policy.CapabilityCommandHelp,
			Permission: "public",
			Handler: func(_ context.Context, request command.Request, _ command.Context) error {
				called = true
				if request.Name != "help" {
					t.Fatalf("handler request name = %q, want canonical help", request.Name)
				}
				return nil
			},
		},
	})
	if err != nil {
		t.Fatalf("create registry: %v", err)
	}
	if err := registry.Dispatch(context.Background(), command.Request{Name: "MENU"}, command.Context{}); err != nil {
		t.Fatalf("dispatch alias: %v", err)
	}
	if !called {
		t.Fatal("registered handler was not called")
	}
}

func TestRegistryEnforcesPermissionForHumanAndBotOrigins(t *testing.T) {
	called := false
	registry, err := command.NewRegistry([]command.Descriptor{
		{
			Name:       "help",
			Capability: policy.CapabilityCommandHelp,
			Permission: "public and !fromMe",
			Handler: func(_ context.Context, _ command.Request, _ command.Context) error {
				called = true
				return nil
			},
		},
	})
	if err != nil {
		t.Fatalf("create registry: %v", err)
	}
	request, _, recognized := registry.Parse("/help")
	if !recognized {
		t.Fatal("help was not recognized")
	}
	if err := registry.Dispatch(context.Background(), request, command.Context{Facts: command.PermissionFacts{FromMe: true}}); err == nil || !agent.IsCode(err, agent.ErrorPermissionDenied) {
		t.Fatalf("bot dispatch error = %v, want permission_denied", err)
	}
	if called {
		t.Fatal("permission-denied handler was called")
	}
	if err := registry.Dispatch(context.Background(), request, command.Context{}); err != nil {
		t.Fatalf("human dispatch: %v", err)
	}
	if !called {
		t.Fatal("allowed handler was not called")
	}
}

func TestRegistryAllowsBotWhenDescriptorExplicitlyGrantsFromMe(t *testing.T) {
	called := false
	registry, err := command.NewRegistry([]command.Descriptor{
		{
			Name:       "internal",
			Capability: policy.CapabilityCommandInfo,
			Permission: "fromMe",
			Handler: func(_ context.Context, _ command.Request, _ command.Context) error {
				called = true
				return nil
			},
		},
	})
	if err != nil {
		t.Fatalf("create registry: %v", err)
	}
	request, _, recognized := registry.Parse("/internal")
	if !recognized {
		t.Fatal("internal command was not recognized")
	}
	if err := registry.Dispatch(context.Background(), request, command.Context{Facts: command.PermissionFacts{FromMe: true}}); err != nil {
		t.Fatalf("explicit bot dispatch: %v", err)
	}
	if !called {
		t.Fatal("explicitly allowed bot handler was not called")
	}
}

func TestRegistryRejectsInvalidPermissionAtConstruction(t *testing.T) {
	_, err := command.NewRegistry([]command.Descriptor{
		{Name: "help", Capability: policy.CapabilityCommandHelp, Permission: "public and"},
	})
	if err == nil || !agent.IsCode(err, agent.ErrorInvalidArgument) {
		t.Fatalf("invalid permission error = %v, want invalid_argument", err)
	}
}
