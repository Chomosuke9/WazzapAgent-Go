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

var InfoCommand = command.Descriptor{
	Name:        "info",
	Capability:  policy.CapabilityCommandInfo,
	Permission:  "public",
	Description: "Shows the Agent, model, configuration, and history status.",
	Handler:     handleInfo,
}

func handleInfo(ctx context.Context, input command.Context, adapter command.Adapter) error {
	send := func(text string) error {
		actionID, err := identity.NewActionID()
		if err != nil {
			return agent.NewError(agent.ErrorInternal, "create info response ID", err)
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
	page, err := input.Agent.History().List(ctx, input.Snapshot.Version, agent.HistoryQuery{Limit: 1})
	if err != nil {
		return err
	}
	historyState := "empty"
	if len(page.Entries) > 0 {
		historyState = "active"
	}
	response := fmt.Sprintf("Agent is active. Model: %s. Config version: %d. History: %s.", input.Snapshot.Model.Model, input.Snapshot.Version, historyState)
	return send(response)
}
