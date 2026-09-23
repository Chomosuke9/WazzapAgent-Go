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

var ResetCommand = command.Descriptor{
	Name:        "reset",
	Capability:  policy.CapabilityHistoryReset,
	Permission:  "owner and !fromMe",
	Description: "Clears the conversation history for this chat.",
	DeniedReply: "The /reset command can only be used by the configured owner.",
	Handler:     handleReset,
}

func handleReset(ctx context.Context, input command.Context, adapter command.Adapter) error {
	send := func(text string) error {
		actionID, err := identity.NewActionID()
		if err != nil {
			return agent.NewError(agent.ErrorInternal, "create reset response ID", err)
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
	if err := input.Agent.History().Reset(ctx, input.Snapshot.Version); err != nil {
		return err
	}
	input.Observer.ObserveHistoryReset()
	return send("Conversation history was reset.")
}
