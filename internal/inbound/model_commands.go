package inbound

import (
	"context"
	"errors"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/action"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/effect"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/policy"
)

type ModelCommandPolicy interface {
	CommandPermissionFacts(context.Context, policy.Principal, agent.PermissionConfig, bool) (policy.PermissionFacts, error)
}

// ModelCommandExecutor runs reply_message.command through the same registry and
// permission expression as an inbound slash command. The principal remains the
// WhatsApp bot/model, so fromMe is always true here.
type ModelCommandExecutor struct {
	factory  agent.Factory
	policy   ModelCommandPolicy
	platform command.Platform
	observer Observer
	clock    agent.Clock
}

func NewModelCommandExecutor(factory agent.Factory, gate ModelCommandPolicy, platform command.Platform, observer Observer, clock agent.Clock) (*ModelCommandExecutor, error) {
	if factory == nil || gate == nil || platform.Text == nil || observer == nil || clock == nil {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "create model command executor", errors.New("command dependencies are required"))
	}
	return &ModelCommandExecutor{factory: factory, policy: gate, platform: platform, observer: observer, clock: clock}, nil
}

// ExecuteCommandEffect runs one model-requested command. The registry's
// permission expression, evaluated with fromMe=true inside Dispatch, is the
// only authorization. The command runs on a fresh Agent because the chat's
// Agent is still inside the turn that requested it.
func (executor *ModelCommandExecutor) ExecuteCommandEffect(ctx context.Context, stored effect.Stored, value effect.RunCommand) (string, error) {
	request, _, recognized := builtinCommandRegistry.Parse(value.Command)
	if !recognized {
		return "", agent.NewError(agent.ErrorInvalidArgument, "run model command", errors.New("command is not registered"))
	}
	current, err := executor.factory.NewAgent(ctx, stored.Request.Ref.Key)
	if err != nil {
		return "", err
	}
	snapshot, err := current.Config().Refresh(ctx)
	if err != nil {
		return "", err
	}
	facts, err := executor.policy.CommandPermissionFacts(ctx, stored.Request.Principal, snapshot.Permission, true)
	if err != nil {
		return "", err
	}
	messageID, err := identity.NewMessageID()
	if err != nil {
		return "", agent.NewError(agent.ErrorInternal, "create model command message", err)
	}
	causationID, err := identity.NewCausationID()
	if err != nil {
		return "", agent.NewError(agent.ErrorInternal, "create model command causation", err)
	}
	chatKind := conversation.ChatDirect
	if facts.IsGroup {
		chatKind = conversation.ChatGroup
	}
	message := conversation.IncomingMessage{
		ID: messageID, InvocationID: stored.Request.InvocationID, CausationID: causationID,
		TenantID: stored.Request.Ref.Key.TenantID, AccountID: stored.Request.Ref.Key.AccountID, ChatID: stored.Request.Ref.Key.ChatID,
		ChatKind: chatKind, Text: value.Command, FromMe: true, Allowlisted: true,
		OccurredAt: executor.clock.Now(), ReceivedAt: executor.clock.Now(),
	}
	if !value.TargetMessageID.IsZero() {
		message.Quote = &conversation.QuotedMessage{ID: value.TargetMessageID, Role: conversation.QuoteUser, Text: "command target"}
	}
	// Outside a batch, the command is reported in a message of its own.
	batch, batched := ctx.Value(commandBatchKey{}).(*commandBatch)
	if !batched {
		batch = &commandBatch{}
	}
	batch.key = stored.Request.Ref.Key
	// A model-issued command has no inbox record, so it runs without a Store:
	// there is no inbox record to mark handled.
	err = builtinCommandRegistry.Dispatch(ctx, request, command.Invocation{
		Agent: current, Config: snapshot, Message: message, Facts: facts,
		Platform: executor.platform, Observer: executor.observer, Notices: &batch.notices,
	})
	if !batched {
		err = errors.Join(err, executor.sendNotices(ctx, batch))
	}
	if err != nil {
		return "", err
	}
	return "command:" + request.Name, nil
}

// commandBatch collects the notices of the commands in one model reply.
type commandBatch struct {
	key     agent.Key
	notices command.NoticeBatch
}

type commandBatchKey struct{}

// BeginCommandBatch makes the commands run with the returned context report
// what they did in one message, sent by the returned function.
func (executor *ModelCommandExecutor) BeginCommandBatch(ctx context.Context) (context.Context, func(context.Context) error) {
	batch := &commandBatch{}
	return context.WithValue(ctx, commandBatchKey{}, batch), func(ctx context.Context) error {
		return executor.sendNotices(ctx, batch)
	}
}

func (executor *ModelCommandExecutor) sendNotices(ctx context.Context, batch *commandBatch) error {
	text := batch.notices.Text()
	if text == "" {
		return nil
	}
	actionID, err := identity.NewActionID()
	if err != nil {
		return agent.NewError(agent.ErrorInternal, "create command notice ID", err)
	}
	_, err = executor.platform.Text.SendText(ctx, action.SendTextRequest{Key: batch.key, ActionID: actionID, Text: text})
	return err
}
