package commands

import (
	"context"
	"testing"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
)

func TestTestButtonSendsButtonsAndAnswersTaps(t *testing.T) {
	registry := builtinRegistry(t)
	key := testChatKey(t)
	run := func(text string) (*recordingText, *recordingButtons, error) {
		t.Helper()
		request, _, recognized := registry.Parse(text)
		if !recognized {
			t.Fatalf("%q is not registered", text)
		}
		sentText, sentButtons := &recordingText{}, &recordingButtons{}
		err := registry.Dispatch(context.Background(), request, command.Invocation{
			Message:  conversation.IncomingMessage{TenantID: key.TenantID, AccountID: key.AccountID, ChatID: key.ChatID, Text: text},
			Facts:    command.PermissionFacts{IsPrivate: true},
			Platform: command.Platform{Text: sentText, Buttons: sentButtons},
		})
		return sentText, sentButtons, err
	}

	_, buttons, err := run("/test-button")
	if err != nil || len(buttons.sent) != 1 || len(buttons.sent[0].Buttons) != 3 {
		t.Fatalf("err=%v buttons=%#v", err, buttons.sent)
	}
	// Tapping each button sends its ID back through the router.
	for _, button := range buttons.sent[0].Buttons {
		text, _, err := run(button.ID)
		want := "Button works. You tapped " + button.Label + "."
		if err != nil || len(text.sent) != 1 || text.sent[0] != want {
			t.Fatalf("tap %q: err=%v sent=%q, want %q", button.ID, err, text.sent, want)
		}
	}

	text, _, err := run("/test-button z")
	if err != nil || len(text.sent) != 1 || text.sent[0] != "Usage: /test-button, then tap one of the buttons." {
		t.Fatalf("unknown choice: err=%v sent=%q", err, text.sent)
	}
}
