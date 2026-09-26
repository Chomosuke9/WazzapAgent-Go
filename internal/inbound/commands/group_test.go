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
	now         time.Time
	err         error
	sent        []action.SendTextRequest
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
func (fake *fakeGroupModerator) MuteGroupMember(_ context.Context, key agent.Key, ref identity.SenderRef, minutes uint32, now time.Time) error {
	fake.ref, fake.minutes, fake.now = ref, minutes, now
	return fake.record("mute", key)
}
func (fake *fakeGroupModerator) SendText(_ context.Context, request action.SendTextRequest) (action.SendTextResult, error) {
	fake.sent = append(fake.sent, request)
	return action.SendTextResult{}, nil
}

// textOnlyAdapter satisfies command.Adapter but not GroupModerator.
type textOnlyAdapter struct{}

func (textOnlyAdapter) SendText(context.Context, action.SendTextRequest) (action.SendTextResult, error) {
	return action.SendTextResult{}, nil
}

type recordingCommandStore struct{ handled int }

func (store *recordingCommandStore) MarkCommandHandled(context.Context, conversation.IncomingMessage) error {
	store.handled++
	return nil
}
func (*recordingCommandStore) BeginPromptMutation(context.Context, conversation.IncomingMessage, command.PromptCommand, agent.ConfigVersion) (command.PromptMutation, error) {
	return command.PromptMutation{}, nil
}
func (*recordingCommandStore) MarkPromptMutationApplied(context.Context, conversation.IncomingMessage, agent.ConfigVersion, agent.ConfigVersion) error {
	return nil
}
func (*recordingCommandStore) BeginPermissionMutation(context.Context, conversation.IncomingMessage, command.PermissionCommand, agent.ConfigVersion) (command.PromptMutation, error) {
	return command.PromptMutation{}, nil
}
func (*recordingCommandStore) MarkPermissionMutationApplied(context.Context, conversation.IncomingMessage, agent.ConfigVersion, agent.ConfigVersion) error {
	return nil
}
func (*recordingCommandStore) BeginTriggerMutation(context.Context, conversation.IncomingMessage, command.TriggerCommand, agent.ConfigVersion) (command.PromptMutation, error) {
	return command.PromptMutation{}, nil
}
func (*recordingCommandStore) MarkTriggerMutationApplied(context.Context, conversation.IncomingMessage, agent.ConfigVersion, agent.ConfigVersion) error {
	return nil
}

func TestHandleGroupOwnsEverySupportedSubcommand(t *testing.T) {
	key := groupCommandKey(t)
	target, _ := identity.NewMessageID()
	now := time.Date(2026, 9, 16, 1, 2, 3, 0, time.UTC)
	tests := []struct {
		name, command, operation string
		target                   identity.MessageID
		check                    func(*testing.T, *fakeGroupModerator)
	}{
		{name: "close", command: "/group close", operation: "announce", check: func(t *testing.T, got *fakeGroupModerator) {
			if !got.announce {
				t.Fatal("close did not enable announce mode")
			}
		}},
		{name: "open", command: "/group open", operation: "announce", check: func(t *testing.T, got *fakeGroupModerator) {
			if got.announce {
				t.Fatal("open enabled announce mode")
			}
		}},
		{name: "description", command: "/group description Aturan baru", operation: "description", check: func(t *testing.T, got *fakeGroupModerator) {
			if got.description != "Aturan baru" {
				t.Fatalf("description = %q", got.description)
			}
		}},
		{name: "delete", command: "/group delete", target: target, operation: "delete", check: func(t *testing.T, got *fakeGroupModerator) {
			if got.target != target {
				t.Fatalf("revoked %v, want %v", got.target, target)
			}
		}},
		{name: "mute", command: "/group mute @Alice (abc123) 15", operation: "mute", check: func(t *testing.T, got *fakeGroupModerator) {
			if got.ref.String() != "abc123" || got.minutes != 15 || !got.now.Equal(now) {
				t.Fatal("wrong mute arguments")
			}
		}},
		{name: "kick", command: "/group kick @Alice Smith (abc123)", operation: "kick", check: func(t *testing.T, got *fakeGroupModerator) {
			if got.ref.String() != "abc123" {
				t.Fatalf("kicked %q, want abc123", got.ref)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			moderator := &fakeGroupModerator{}
			if err := HandleGroup(context.Background(), moderator, key, test.command, test.target, now); err != nil {
				t.Fatalf("handle command: %v", err)
			}
			if moderator.operation != test.operation {
				t.Fatalf("operation = %q, want %q", moderator.operation, test.operation)
			}
			if moderator.key != key {
				t.Fatalf("key = %v, want %v", moderator.key, key)
			}
			test.check(t, moderator)
		})
	}
}

func TestHandleGroupRejectsMalformedCommandsBeforeProviderCall(t *testing.T) {
	key := groupCommandKey(t)
	target, _ := identity.NewMessageID()
	tests := []struct {
		command string
		target  identity.MessageID
	}{
		{command: "/group close now"},
		{command: "/group description"},
		{command: "/group delete"},
		{command: "/group close", target: target},
		{command: "/group mute @Alice (abc123) 43201"},
		{command: "/group kick @Alice (invalid)"},
		{command: "/group admin"},
	}
	for _, test := range tests {
		moderator := &fakeGroupModerator{}
		err := HandleGroup(context.Background(), moderator, key, test.command, test.target, time.Now().UTC())
		if !agent.IsCode(err, agent.ErrorInvalidArgument) {
			t.Fatalf("%q: error = %v, want invalid argument", test.command, err)
		}
		if moderator.operation != "" {
			t.Fatalf("moderator called for malformed command %q", test.command)
		}
	}
}

func TestHandleGroupRejectsMissingDependencies(t *testing.T) {
	key := groupCommandKey(t)
	now := time.Now().UTC()
	if err := HandleGroup(context.Background(), nil, key, "/group close", identity.MessageID{}, now); !agent.IsCode(err, agent.ErrorInvalidArgument) {
		t.Fatalf("nil moderator error = %v", err)
	}
	moderator := &fakeGroupModerator{}
	if err := HandleGroup(context.Background(), moderator, key, "/group close", identity.MessageID{}, time.Time{}); !agent.IsCode(err, agent.ErrorInvalidArgument) {
		t.Fatalf("zero time error = %v", err)
	}
	if err := HandleGroup(context.Background(), moderator, agent.Key{}, "/group close", identity.MessageID{}, now); !agent.IsCode(err, agent.ErrorInvalidArgument) {
		t.Fatalf("zero key error = %v", err)
	}
	if moderator.operation != "" {
		t.Fatal("moderator called without valid dependencies")
	}
}

func TestHandleGroupPropagatesModeratorErrors(t *testing.T) {
	want := agent.NewError(agent.ErrorProviderFailure, "set group announce", errors.New("boom"))
	moderator := &fakeGroupModerator{err: want}
	err := HandleGroup(context.Background(), moderator, groupCommandKey(t), "/group close", identity.MessageID{}, time.Now().UTC())
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func TestGroupCommandSuppressesAutomaticFeedbackForDeleteAndKick(t *testing.T) {
	key := groupCommandKey(t)
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
		{name: "usage on malformed", text: "/group admin", wantSent: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			moderator := &fakeGroupModerator{}
			store := &recordingCommandStore{}
			if err := handleGroup(context.Background(), groupCommandInput(key, test.text, test.quote, store), moderator); err != nil {
				t.Fatalf("handle group command: %v", err)
			}
			if len(moderator.sent) != test.wantSent {
				t.Fatalf("automatic feedback messages = %d, want %d", len(moderator.sent), test.wantSent)
			}
			if store.handled != 1 {
				t.Fatalf("handled marks = %d, want 1", store.handled)
			}
		})
	}
}

func TestGroupCommandRequiresModerator(t *testing.T) {
	store := &recordingCommandStore{}
	err := handleGroup(context.Background(), groupCommandInput(groupCommandKey(t), "/group close", nil, store), textOnlyAdapter{})
	if !agent.IsCode(err, agent.ErrorIntegrityFailure) {
		t.Fatalf("error = %v, want integrity failure", err)
	}
	if store.handled != 0 {
		t.Fatal("command marked handled without a moderator")
	}
}

func TestGroupCommandDoesNotMarkHandledOnModeratorFailure(t *testing.T) {
	moderator := &fakeGroupModerator{err: agent.NewError(agent.ErrorProviderFailure, "kick", errors.New("boom"))}
	store := &recordingCommandStore{}
	err := handleGroup(context.Background(), groupCommandInput(groupCommandKey(t), "/group kick @Alice (abc123)", nil, store), moderator)
	if !agent.IsCode(err, agent.ErrorProviderFailure) {
		t.Fatalf("error = %v, want provider failure", err)
	}
	if store.handled != 0 || len(moderator.sent) != 0 {
		t.Fatal("failed command was acknowledged")
	}
}

func groupCommandInput(key agent.Key, text string, quote *conversation.QuotedMessage, store *recordingCommandStore) command.Context {
	return command.Context{
		Message: conversation.IncomingMessage{
			TenantID: key.TenantID, AccountID: key.AccountID, ChatID: key.ChatID,
			Text: text, Quote: quote,
		},
		Store: store,
	}
}

func groupCommandKey(t *testing.T) agent.Key {
	t.Helper()
	tenantID, _ := identity.NewTenantID()
	accountID, _ := identity.NewAccountID()
	chatID, _ := identity.NewChatID()
	return agent.Key{TenantID: tenantID, AccountID: accountID, ChatID: chatID}
}
