package inbound

import (
	"fmt"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
	builtincommands "github.com/Chomosuke9/WazzapAgent-Go/internal/inbound/commands"
)

// builtinCommandRegistry holds every command file in internal/inbound/commands.
// The inbound package owns the boundary concerns (resume, authorization, and
// wiring); what a command does lives in its own file.
var builtinCommandRegistry = mustCommandRegistry(builtincommands.All())

func mustCommandRegistry(commands []command.Command) *command.Registry {
	registry, err := command.NewRegistry(commands)
	if err != nil {
		// Commands are compiled in. A failure is a programmer error, never
		// user input or runtime configuration.
		panic(fmt.Sprintf("invalid built-in command registry: %v", err))
	}
	return registry
}

func parseRegisteredCommand(text string) (command.Request, command.Command, bool) {
	return builtinCommandRegistry.Parse(text)
}

func CommandRegistry() *command.Registry { return builtinCommandRegistry }
