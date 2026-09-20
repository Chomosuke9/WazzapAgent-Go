package inbound

import (
	"fmt"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
	builtincommands "github.com/Chomosuke9/WazzapAgent-Go/internal/inbound/commands"
)

// builtinCommandRegistry is generated from one descriptor per file in
// internal/inbound/commands. The inbound package owns the boundary concerns
// (resume, authorization, and service wiring); command semantics live beside
// each command's descriptor and handler.
var builtinCommandRegistry = mustCommandRegistry(builtincommands.Descriptors)

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

func CommandRegistry() *command.Registry { return builtinCommandRegistry }

// The aliases below preserve the inbound package API used by persistence and
// existing callers while keeping command grammar/types in internal/command.
type PromptMutation = command.PromptMutation

type PermissionCommandKind = command.PermissionCommandKind

const (
	PermissionInvalid = command.PermissionInvalid
	PermissionView    = command.PermissionView
	PermissionSet     = command.PermissionSet
)

type PermissionCommand = command.PermissionCommand

type PromptCommandKind = command.PromptCommandKind

const (
	PromptInvalid = command.PromptInvalid
	PromptView    = command.PromptView
	PromptSet     = command.PromptSet
	PromptClear   = command.PromptClear
)

type PromptCommand = command.PromptCommand
