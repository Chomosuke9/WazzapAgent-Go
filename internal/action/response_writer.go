package action

import (
	"context"
	"fmt"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
)

type CommandStore interface {
	FindCommandResponse(context.Context, conversation.IncomingMessage) (agent.DispatchRef, bool, error)
	PlanCommandResponse(context.Context, conversation.IncomingMessage, agent.ConfigVersion, string) (agent.DispatchRef, error)
}

type CommandResponder struct {
	store      CommandStore
	dispatcher agent.ResponseDispatcher
}

func NewCommandResponder(store CommandStore, dispatcher agent.ResponseDispatcher) (*CommandResponder, error) {
	if store == nil || dispatcher == nil {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "create command responder", fmt.Errorf("store and dispatcher are required"))
	}
	return &CommandResponder{store: store, dispatcher: dispatcher}, nil
}

func (responder *CommandResponder) Resume(ctx context.Context, message conversation.IncomingMessage) (bool, error) {
	ref, found, err := responder.store.FindCommandResponse(ctx, message)
	if err != nil || !found {
		return found, err
	}
	_, err = responder.dispatcher.Dispatch(ctx, ref)
	return true, err
}

func (responder *CommandResponder) Reply(
	ctx context.Context,
	message conversation.IncomingMessage,
	version agent.ConfigVersion,
	text string,
) error {
	ref, err := responder.store.PlanCommandResponse(ctx, message, version, text)
	if err != nil {
		return err
	}
	_, err = responder.dispatcher.Dispatch(ctx, ref)
	return err
}
