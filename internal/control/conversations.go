package control

import (
	"context"
	"errors"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

const (
	conversationListLimit = 100
	conversationPageLimit = 100
)

type BotConversation struct {
	ID                  identity.ChatID
	Kind                string
	Name                string
	LastMessage         string
	LastMessageAt       string
	LastFromBot         bool
	MessageCount        uint64
	LastMessageMentions []BotMention
}

type BotMention struct {
	Token       string
	SenderRef   identity.SenderRef
	DisplayName string
	Bot         bool
}

type BotQuote struct {
	MessageID identity.MessageID
	Role      string
	Sender    string
	Content   string
	Mentions  []BotMention
}

type BotMessage struct {
	ID        identity.MessageID
	Role      string
	Sender    string
	SenderRef identity.SenderRef
	Content   string
	CreatedAt string
	Delivery  string
	Deleted   bool
	Mentions  []BotMention
	Quote     *BotQuote
}

type ConversationRepository interface {
	ListBotConversations(context.Context, SessionScope, uint32) ([]BotConversation, error)
	ListBotMessages(context.Context, SessionScope, identity.ChatID, uint32) ([]BotMessage, error)
}

// ConversationController exposes only the active account's stored transcript.
// It is read-only and resolves account scope from the durable session binding,
// never from a caller-provided tenant or account identifier.
type ConversationController struct {
	bindings SessionBindingRepository
	reader   ConversationRepository
}

func NewConversationController(bindings SessionBindingRepository, reader ConversationRepository) (*ConversationController, error) {
	if bindings == nil || reader == nil {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "create conversation controller", errors.New("session binding repository and transcript reader are required"))
	}
	return &ConversationController{bindings: bindings, reader: reader}, nil
}

func (controller *ConversationController) List(ctx context.Context) ([]BotConversation, error) {
	if controller == nil || controller.bindings == nil || controller.reader == nil {
		return nil, agent.NewError(agent.ErrorUnavailable, "list bot conversations", errors.New("conversation controller is unavailable"))
	}
	binding, err := controller.bindings.LoadSessionBinding(nonNilContext(ctx))
	if err != nil {
		return nil, sessionRepositoryError("load account scope for conversations", err)
	}
	if !binding.HasActiveScope {
		return []BotConversation{}, nil
	}
	conversations, err := controller.reader.ListBotConversations(nonNilContext(ctx), binding.ActiveScope, conversationListLimit)
	if err != nil {
		return nil, conversationRepositoryError("list bot conversations", err)
	}
	return conversations, nil
}

func (controller *ConversationController) Messages(ctx context.Context, chatID string) ([]BotMessage, error) {
	if controller == nil || controller.bindings == nil || controller.reader == nil {
		return nil, agent.NewError(agent.ErrorUnavailable, "list bot messages", errors.New("conversation controller is unavailable"))
	}
	id, err := identity.ParseChatID(chatID)
	if err != nil {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "list bot messages", errors.New("conversation identifier is invalid"))
	}
	binding, err := controller.bindings.LoadSessionBinding(nonNilContext(ctx))
	if err != nil {
		return nil, sessionRepositoryError("load account scope for messages", err)
	}
	if !binding.HasActiveScope {
		return []BotMessage{}, nil
	}
	messages, err := controller.reader.ListBotMessages(nonNilContext(ctx), binding.ActiveScope, id, conversationPageLimit)
	if err != nil {
		return nil, conversationRepositoryError("list bot messages", err)
	}
	return messages, nil
}

func conversationRepositoryError(operation string, err error) error {
	if err == nil {
		return nil
	}
	code := agent.CodeOf(err)
	if code == agent.ErrorInvalidArgument || code == agent.ErrorIntegrityFailure || code == agent.ErrorNotFound {
		return agent.NewError(code, operation, errors.New("conversation data is unavailable or invalid"))
	}
	return agent.NewError(agent.ErrorStorageFailure, operation, errors.New("conversation data could not be read"))
}
