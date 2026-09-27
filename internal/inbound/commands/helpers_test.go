package commands

import (
	"context"
	"testing"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/action"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

// Shared helpers for command tests. See README.md, "Testing a command".

// builtinRegistry is the registry built from every command file.
func builtinRegistry(t *testing.T) *command.Registry {
	t.Helper()
	registry, err := command.NewRegistry(All())
	if err != nil {
		t.Fatalf("create registry: %v", err)
	}
	return registry
}

// recordingText, recordingButtons and handledStore stand in for the
// WhatsApp adapter and the inbox store.
type recordingText struct{ sent []string }

func (text *recordingText) SendText(_ context.Context, request action.SendTextRequest) (action.SendTextResult, error) {
	text.sent = append(text.sent, request.Text)
	return action.SendTextResult{}, nil
}

type recordingButtons struct{ sent []action.SendButtonsRequest }

func (buttons *recordingButtons) SendButtons(_ context.Context, request action.SendButtonsRequest) (action.SendTextResult, error) {
	buttons.sent = append(buttons.sent, request)
	return action.SendTextResult{}, nil
}

type handledStore struct {
	command.Store
	handled int
}

func (store *handledStore) MarkCommandHandled(context.Context, conversation.IncomingMessage) error {
	store.handled++
	return nil
}

// testChatKey returns a fresh, valid chat key.
func testChatKey(t *testing.T) agent.Key {
	t.Helper()
	tenantID, _ := identity.NewTenantID()
	accountID, _ := identity.NewAccountID()
	chatID, _ := identity.NewChatID()
	return agent.Key{TenantID: tenantID, AccountID: accountID, ChatID: chatID}
}
