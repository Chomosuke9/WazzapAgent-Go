package inbound

import (
	"context"
	"errors"

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
	adapter  command.Adapter
	observer Observer
	clock    agent.Clock
}

func NewModelCommandExecutor(factory agent.Factory, gate ModelCommandPolicy, adapter command.Adapter, observer Observer, clock agent.Clock) (*ModelCommandExecutor, error) {
	if factory == nil || gate == nil || adapter == nil || observer == nil || clock == nil {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "create model command executor", errors.New("command dependencies are required"))
	}
	return &ModelCommandExecutor{factory: factory, policy: gate, adapter: adapter, observer: observer, clock: clock}, nil
}

func (executor *ModelCommandExecutor) authorize(ctx context.Context, current *agent.Agent, stored effect.Stored, value effect.RunCommand) (command.Request, command.Descriptor, policy.PermissionFacts, error) {
	if stored.Request.Principal.Kind != policy.PrincipalModel {
		return command.Request{}, command.Descriptor{}, policy.PermissionFacts{}, agent.NewError(agent.ErrorPermissionDenied, "authorize model command", errors.New("model principal is required"))
	}
	snapshot, err := current.Config().Refresh(ctx)
	if err != nil {
		return command.Request{}, command.Descriptor{}, policy.PermissionFacts{}, err
	}
	facts, err := executor.policy.CommandPermissionFacts(ctx, stored.Request.Principal, snapshot.Permission, true)
	if err != nil {
		return command.Request{}, command.Descriptor{}, policy.PermissionFacts{}, err
	}
	request, descriptor, recognized := builtinCommandRegistry.Parse(value.Command)
	if !recognized {
		return command.Request{}, command.Descriptor{}, policy.PermissionFacts{}, agent.NewError(agent.ErrorPermissionDenied, "authorize model command", errors.New("command is not registered"))
	}
	allowed, err := builtinCommandRegistry.Allows(request, facts)
	if err != nil {
		return command.Request{}, command.Descriptor{}, policy.PermissionFacts{}, err
	}
	if !allowed {
		return command.Request{}, command.Descriptor{}, policy.PermissionFacts{}, agent.NewError(agent.ErrorPermissionDenied, "authorize model command", errors.New("command permission denied bot origin"))
	}
	return request, descriptor, facts, nil
}

func (executor *ModelCommandExecutor) AuthorizeCommandEffect(ctx context.Context, stored effect.Stored, value effect.RunCommand) error {
	current, err := executor.factory.NewAgent(ctx, stored.Request.Ref.Key)
	if err != nil {
		return err
	}
	_, _, _, err = executor.authorize(ctx, current, stored, value)
	return err
}

func (executor *ModelCommandExecutor) ExecuteCommandEffect(ctx context.Context, stored effect.Stored, value effect.RunCommand) (string, error) {
	current, err := executor.factory.NewAgent(ctx, stored.Request.Ref.Key)
	if err != nil {
		return "", err
	}
	request, _, facts, err := executor.authorize(ctx, current, stored, value)
	if err != nil {
		return "", err
	}
	snapshot, err := current.Config().Refresh(ctx)
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
	err = builtinCommandRegistry.Dispatch(ctx, request, command.Context{
		Agent: current, Snapshot: snapshot, Message: message, Facts: facts,
		Registry: builtinCommandRegistry, Store: modelCommandStore{}, Observer: executor.observer, Adapter: executor.adapter,
	})
	if err != nil {
		return "", err
	}
	return "command:" + string(request.Name), nil
}

type modelCommandStore struct{}

func (modelCommandStore) MarkCommandHandled(context.Context, conversation.IncomingMessage) error {
	return nil
}
func (modelCommandStore) BeginPromptMutation(_ context.Context, _ conversation.IncomingMessage, _ command.PromptCommand, version agent.ConfigVersion) (command.PromptMutation, error) {
	return command.PromptMutation{ExpectedVersion: version}, nil
}
func (modelCommandStore) MarkPromptMutationApplied(context.Context, conversation.IncomingMessage, agent.ConfigVersion, agent.ConfigVersion) error {
	return nil
}
func (modelCommandStore) BeginPermissionMutation(_ context.Context, _ conversation.IncomingMessage, _ command.PermissionCommand, version agent.ConfigVersion) (command.PromptMutation, error) {
	return command.PromptMutation{ExpectedVersion: version}, nil
}
func (modelCommandStore) MarkPermissionMutationApplied(context.Context, conversation.IncomingMessage, agent.ConfigVersion, agent.ConfigVersion) error {
	return nil
}
func (modelCommandStore) BeginTriggerMutation(_ context.Context, _ conversation.IncomingMessage, _ command.TriggerCommand, version agent.ConfigVersion) (command.PromptMutation, error) {
	return command.PromptMutation{ExpectedVersion: version}, nil
}
func (modelCommandStore) MarkTriggerMutationApplied(context.Context, conversation.IncomingMessage, agent.ConfigVersion, agent.ConfigVersion) error {
	return nil
}
