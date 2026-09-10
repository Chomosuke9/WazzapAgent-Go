package inbound

import (
	"fmt"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/policy"
)

var builtinCommandRegistry = mustCommandRegistry([]command.Descriptor{
	{Name: "help", Aliases: []string{"menu"}, Capability: policy.CapabilityCommandHelp},
	{Name: "info", Capability: policy.CapabilityCommandInfo},
	{Name: "reset", Capability: policy.CapabilityHistoryReset},
	{Name: "prompt", Capability: policy.CapabilityPromptWrite},
	{Name: "permission", Aliases: []string{"permissions"}, Capability: policy.CapabilityPermissionWrite},
})

func mustCommandRegistry(descriptors []command.Descriptor) *command.Registry {
	registry, err := command.NewRegistry(descriptors)
	if err != nil {
		// These are compile-time descriptors. A failure is a programmer error,
		// never user input or runtime configuration.
		panic(fmt.Sprintf("invalid built-in command registry: %v", err))
	}
	return registry
}

func parseRegisteredCommand(text string) (command.Request, command.Descriptor, bool) {
	return builtinCommandRegistry.Parse(text)
}

func canonicalCommandText(request command.Request) string {
	text := "/" + string(request.Name)
	if request.ArgumentsPresent {
		return text + " " + request.Arguments
	}
	return text
}

func parseControlRequest(request command.Request) (ControlCommandKind, bool) {
	if request.ArgumentsPresent {
		return 0, false
	}
	return ParseControlCommand(canonicalCommandText(request))
}

func invalidControlReply(request command.Request) string {
	return fmt.Sprintf("Format perintah /%s tidak menerima argumen.", request.Name)
}

// commandCapabilityMismatch is a defensive integrity error. It means a
// developer changed the declarative registry without changing the semantic
// handler; unrecognized command text itself is never an integrity failure.
func commandCapabilityMismatch(request command.Request, capability policy.Capability) error {
	return agent.NewError(agent.ErrorIntegrityFailure, "dispatch registered command", fmt.Errorf("command %q has unexpected capability %q", request.Name, capability))
}
