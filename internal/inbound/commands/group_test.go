package commands

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/action"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

// fakeGroupModerator records every port call so tests can assert dispatch
// without any provider types.
type fakeGroupModerator struct {
	operation   string
	key         agent.Key
	announce    bool
	description string
	target      identity.MessageID
	ref         identity.SenderRef
	minutes     uint32
	err         error
}

func (fake *fakeGroupModerator) record(operation string, key agent.Key) error {
	fake.operation, fake.key = operation, key
	return fake.err
}

func (fake *fakeGroupModerator) SetGroupAnnounce(_ context.Context, key agent.Key, announce bool) error {
	fake.announce = announce
	return fake.record("announce", key)
}
func (fake *fakeGroupModerator) SetGroupDescription(_ context.Context, key agent.Key, description string) error {
	fake.description = description
	return fake.record("description", key)
}
func (fake *fakeGroupModerator) RevokeGroupMessage(_ context.Context, key agent.Key, target identity.MessageID) error {
	fake.target = target
	return fake.record("delete", key)
}
func (fake *fakeGroupModerator) RemoveGroupMember(_ context.Context, key agent.Key, ref identity.SenderRef) error {
	fake.ref = ref
	return fake.record("kick", key)
}
func (fake *fakeGroupModerator) MuteGroupMember(_ context.Context, key agent.Key, ref identity.SenderRef, minutes uint32, _ time.Time) error {
	fake.ref, fake.minutes = ref, minutes
	return fake.record("mute", key)
}

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

type groupRun struct {
	moderator *fakeGroupModerator
	text      *recordingText
	store     *handledStore
	key       agent.Key
	err       error
}

func runGroupCommand(t *testing.T, text string, quote *conversation.QuotedMessage, moderator *fakeGroupModerator) groupRun {
	t.Helper()
	registry := builtinRegistry(t)
	request, _, recognized := registry.Parse(text)
	if !recognized {
		t.Fatalf("%q was not recognized", text)
	}
	key := groupCommandKey(t)
	run := groupRun{moderator: moderator, text: &recordingText{}, store: &handledStore{}, key: key}
	platform := command.Platform{Text: run.text}
	if moderator != nil {
		platform.Group = moderator
	}
	run.err = registry.Dispatch(context.Background(), request, command.Invocation{
		Message: conversation.IncomingMessage{
			TenantID: key.TenantID, AccountID: key.AccountID, ChatID: key.ChatID, Text: text, Quote: quote,
		},
		Facts:    command.PermissionFacts{IsGroup: true, IsAdmin: true},
		Platform: platform,
		Store:    run.store,
	})
	return run
}

func TestGroupCommandOwnsEverySupportedSubcommand(t *testing.T) {
	target, _ := identity.NewMessageID()
	tests := []struct {
		name, text, operation string
		quote                 *conversation.QuotedMessage
		check                 func(*testing.T, *fakeGroupModerator)
	}{
		{name: "close", text: "/group close", operation: "announce", check: func(t *testing.T, got *fakeGroupModerator) {
			if !got.announce {
				t.Fatal("close did not enable announce mode")
			}
		}},
		{name: "open", text: "/group open", operation: "announce", check: func(t *testing.T, got *fakeGroupModerator) {
			if got.announce {
				t.Fatal("open enabled announce mode")
			}
		}},
		{name: "description", text: "/group description Aturan baru", operation: "description", check: func(t *testing.T, got *fakeGroupModerator) {
			if got.description != "Aturan baru" {
				t.Fatalf("description = %q", got.description)
			}
		}},
		{name: "delete", text: "/group delete", quote: &conversation.QuotedMessage{ID: target}, operation: "delete", check: func(t *testing.T, got *fakeGroupModerator) {
			if got.target != target {
				t.Fatalf("revoked %v, want %v", got.target, target)
			}
		}},
		{name: "mute", text: "/group mute @Alice (abc123) 15", operation: "mute", check: func(t *testing.T, got *fakeGroupModerator) {
			if got.ref.String() != "abc123" || got.minutes != 15 {
				t.Fatal("wrong mute arguments")
			}
		}},
		{name: "kick", text: "/group kick @Alice Smith (abc123)", operation: "kick", check: func(t *testing.T, got *fakeGroupModerator) {
			if got.ref.String() != "abc123" {
				t.Fatalf("kicked %q, want abc123", got.ref)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			run := runGroupCommand(t, test.text, test.quote, &fakeGroupModerator{})
			if run.err != nil {
				t.Fatalf("run command: %v", run.err)
			}
			if run.moderator.operation != test.operation || run.moderator.key != run.key {
				t.Fatalf("operation/key = %q/%v, want %q/%v", run.moderator.operation, run.moderator.key, test.operation, run.key)
			}
			test.check(t, run.moderator)
			if run.store.handled != 1 {
				t.Fatalf("handled marks = %d, want 1", run.store.handled)
			}
		})
	}
}

func TestGroupCommandRepliesWithUsageBeforeProviderCall(t *testing.T) {
	target, _ := identity.NewMessageID()
	for _, test := range []struct {
		text  string
		quote *conversation.QuotedMessage
	}{
		{text: "/group close now"},
		{text: "/group description"},
		{text: "/group delete"},
		{text: "/group mute @Alice (abc123) 43201"},
		{text: "/group kick @Alice (invalid)"},
		{text: "/group admin"},
		{text: "/group"},
	} {
		run := runGroupCommand(t, test.text, test.quote, &fakeGroupModerator{})
		if run.err != nil || run.moderator.operation != "" || len(run.text.sent) != 1 || run.text.sent[0] != groupUsage {
			t.Fatalf("%q: err=%v operation=%q sent=%q", test.text, run.err, run.moderator.operation, run.text.sent)
		}
	}
	// A reply to a message is only a target for delete; other subcommands ignore it.
	run := runGroupCommand(t, "/group close", &conversation.QuotedMessage{ID: target}, &fakeGroupModerator{})
	if run.err != nil || run.moderator.operation != "announce" {
		t.Fatalf("close as a reply: err=%v operation=%q", run.err, run.moderator.operation)
	}
}

func TestGroupCommandSuppressesFeedbackForDeleteAndKick(t *testing.T) {
	target, _ := identity.NewMessageID()
	tests := []struct {
		name     string
		text     string
		quote    *conversation.QuotedMessage
		wantSent int
	}{
		{name: "delete", text: "/group delete", quote: &conversation.QuotedMessage{ID: target}},
		{name: "kick", text: "/group kick @Alice (abc123)"},
		{name: "close still confirms", text: "/group close", wantSent: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			run := runGroupCommand(t, test.text, test.quote, &fakeGroupModerator{})
			if run.err != nil {
				t.Fatalf("run command: %v", run.err)
			}
			if len(run.text.sent) != test.wantSent || run.store.handled != 1 {
				t.Fatalf("sent/handled = %d/%d, want %d/1", len(run.text.sent), run.store.handled, test.wantSent)
			}
		})
	}
}

func TestGroupCommandRequiresModerator(t *testing.T) {
	run := runGroupCommand(t, "/group close", nil, nil)
	if !agent.IsCode(run.err, agent.ErrorUnavailable) || run.store.handled != 0 {
		t.Fatalf("error/handled = %v/%d, want unavailable/0", run.err, run.store.handled)
	}
}

func TestGroupCommandDoesNotMarkHandledOnModeratorFailure(t *testing.T) {
	want := agent.NewError(agent.ErrorProviderFailure, "kick", errors.New("boom"))
	run := runGroupCommand(t, "/group kick @Alice (abc123)", nil, &fakeGroupModerator{err: want})
	if !errors.Is(run.err, want) || run.store.handled != 0 || len(run.text.sent) != 0 {
		t.Fatalf("error/handled/sent = %v/%d/%d", run.err, run.store.handled, len(run.text.sent))
	}
}

func groupCommandKey(t *testing.T) agent.Key {
	t.Helper()
	tenantID, _ := identity.NewTenantID()
	accountID, _ := identity.NewAccountID()
	chatID, _ := identity.NewChatID()
	return agent.Key{TenantID: tenantID, AccountID: accountID, ChatID: chatID}
}
