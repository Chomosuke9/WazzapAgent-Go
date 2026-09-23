package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/action"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/policy"
)

var HelpCommand = command.Descriptor{
	Name:        "help",
	Aliases:     []string{"menu"},
	Capability:  policy.CapabilityCommandHelp,
	Permission:  "public",
	Description: "Shows the list of available commands.",
	Handler:     handleHelp,
}

func handleHelp(ctx context.Context, input command.Context, adapter command.Adapter) error {
	send := func(text string) error {
		actionID, err := identity.NewActionID()
		if err != nil {
			return agent.NewError(agent.ErrorInternal, "create help response ID", err)
		}
		key := agent.Key{TenantID: input.Message.TenantID, AccountID: input.Message.AccountID, ChatID: input.Message.ChatID}
		if _, err := adapter.SendText(ctx, action.SendTextRequest{Key: key, ActionID: actionID, Text: text}); err != nil {
			return err
		}
		return input.Store.MarkCommandHandled(ctx, input.Message)
	}
	token, _, argumentsPresent := strings.Cut(strings.TrimPrefix(input.Message.Text, "/"), " ")
	if argumentsPresent {
		return send(fmt.Sprintf("The /%s command does not accept arguments.", token))
	}
	if input.Registry == nil {
		return agent.NewError(agent.ErrorIntegrityFailure, "handle help command", fmt.Errorf("command registry is required"))
	}
	descriptors := input.Registry.Descriptors()
	lines := []string{"Available commands:"}
	for _, descriptor := range descriptors {
		aliases := ""
		if len(descriptor.Aliases) > 0 {
			aliases = " (alias: /" + strings.Join(descriptor.Aliases, ", /") + ")"
		}
		lines = append(lines, fmt.Sprintf("/%s%s - %s", descriptor.Name, aliases, descriptor.Description))
	}
	return send(strings.Join(lines, "\n"))
}
