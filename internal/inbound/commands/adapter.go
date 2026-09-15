package commands

import (
	"context"
	"errors"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/action"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

// TextAdapter is the provider adapter surface shared by slash commands.
// Commands send their own chat output through this surface.
type TextAdapter interface {
	SendText(context.Context, action.SendTextRequest) (action.SendTextResult, error)
}

func sendText(ctx context.Context, input command.Context, rawAdapter any, text string) error {
	adapter, ok := rawAdapter.(TextAdapter)
	if !ok || adapter == nil {
		return agent.NewError(agent.ErrorIntegrityFailure, "send command response", errors.New("text adapter is unavailable"))
	}
	actionID, err := identity.NewActionID()
	if err != nil {
		return agent.NewError(agent.ErrorInternal, "create command response ID", err)
	}
	key := agent.Key{
		TenantID:  input.Message.TenantID,
		AccountID: input.Message.AccountID,
		ChatID:    input.Message.ChatID,
	}
	_, err = adapter.SendText(ctx, action.SendTextRequest{Key: key, ActionID: actionID, Text: text})
	if err != nil {
		return err
	}
	return input.Store.MarkCommandHandled(ctx, input.Message)
}
