package commands

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Chomosuke9/DiscordAgent-Go/internal/agent"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/command"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/conversation"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/identity"
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
	key := testChatKey(t)
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
		{name: "lock", text: "/mod lock", operation: "announce", check: func(t *testing.T, got *fakeGroupModerator) {
			if !got.announce {
				t.Fatal("lock did not restrict sending")
			}
		}},
		{name: "unlock", text: "/mod unlock", operation: "announce", check: func(t *testing.T, got *fakeGroupModerator) {
			if got.announce {
				t.Fatal("unlock restricted sending")
			}
		}},
		{name: "topic", text: "/mod topic Aturan baru", operation: "description", check: func(t *testing.T, got *fakeGroupModerator) {
			if got.description != "Aturan baru" {
				t.Fatalf("description = %q", got.description)
			}
		}},
		{name: "delete", text: "/mod delete", quote: &conversation.QuotedMessage{ID: target}, operation: "delete", check: func(t *testing.T, got *fakeGroupModerator) {
			if got.target != target {
				t.Fatalf("revoked %v, want %v", got.target, target)
			}
		}},
		{name: "mute", text: "/mod mute @Alice (abc123) 15", operation: "mute", check: func(t *testing.T, got *fakeGroupModerator) {
			if got.ref.String() != "abc123" || got.minutes != 15 {
				t.Fatal("wrong mute arguments")
			}
		}},
		{name: "kick", text: "/mod kick @Alice Smith (abc123)", operation: "kick", check: func(t *testing.T, got *fakeGroupModerator) {
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
		{text: "/mod lock now"},
		{text: "/mod topic"},
		{text: "/mod delete"},
		{text: "/mod mute @Alice (abc123) 43201"},
		{text: "/mod kick @Alice (invalid)"},
		{text: "/mod admin"},
		{text: "/mod"},
	} {
		run := runGroupCommand(t, test.text, test.quote, &fakeGroupModerator{})
		if run.err != nil || run.moderator.operation != "" || len(run.text.sent) != 1 || run.text.sent[0] != modUsage {
			t.Fatalf("%q: err=%v operation=%q sent=%q", test.text, run.err, run.moderator.operation, run.text.sent)
		}
	}
	// A reply to a message is only a target for delete; other subcommands ignore it.
	run := runGroupCommand(t, "/mod lock", &conversation.QuotedMessage{ID: target}, &fakeGroupModerator{})
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
		{name: "delete", text: "/mod delete", quote: &conversation.QuotedMessage{ID: target}},
		{name: "kick", text: "/mod kick @Alice (abc123)"},
		{name: "lock still confirms", text: "/mod lock", wantSent: 1},
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
	run := runGroupCommand(t, "/mod lock", nil, nil)
	if !agent.IsCode(run.err, agent.ErrorUnavailable) || run.store.handled != 0 {
		t.Fatalf("error/handled = %v/%d, want unavailable/0", run.err, run.store.handled)
	}
}

func TestGroupCommandDoesNotMarkHandledOnModeratorFailure(t *testing.T) {
	want := agent.NewError(agent.ErrorProviderFailure, "kick", errors.New("boom"))
	run := runGroupCommand(t, "/mod kick @Alice (abc123)", nil, &fakeGroupModerator{err: want})
	if !errors.Is(run.err, want) || run.store.handled != 0 || len(run.text.sent) != 0 {
		t.Fatalf("error/handled/sent = %v/%d/%d", run.err, run.store.handled, len(run.text.sent))
	}
}

func TestBotGroupCommandsStayWithinTheModerationLevel(t *testing.T) {
	target, _ := identity.NewMessageID()
	quote := &conversation.QuotedMessage{ID: target, Role: conversation.QuoteUser, Text: "spam"}
	tests := []struct {
		text    string
		level   agent.ModerationLevel
		allowed bool
	}{
		{"/mod delete", agent.ModerationNone, false}, {"/mod delete", agent.ModerationDelete, true},
		{"/mod mute @Budi (a1b2c3) 10", agent.ModerationDelete, false}, {"/mod mute @Budi (a1b2c3) 10", agent.ModerationDeleteMute, true},
		{"/mod kick @Budi (a1b2c3)", agent.ModerationDeleteMute, false}, {"/mod kick @Budi (a1b2c3)", agent.ModerationDeleteMuteKick, true},
		{"/mod lock", agent.ModerationNone, true},
	}
	registry := builtinRegistry(t)
	for _, test := range tests {
		request, _, _ := registry.Parse(test.text)
		key := testChatKey(t)
		moderator, text := &fakeGroupModerator{}, &recordingText{}
		err := registry.Dispatch(context.Background(), request, command.Invocation{
			Message: conversation.IncomingMessage{
				TenantID: key.TenantID, AccountID: key.AccountID, ChatID: key.ChatID, Text: test.text, Quote: quote, FromMe: true,
			},
			Config:   agent.ConfigSnapshot{Permission: agent.PermissionConfig{ModerationLevel: test.level}},
			Facts:    command.PermissionFacts{IsGroup: true, IsAdmin: true, BotIsAdmin: true, FromMe: true},
			Platform: command.Platform{Text: text, Group: moderator},
		})
		if err != nil {
			t.Fatalf("%s at level %d: %v", test.text, test.level, err)
		}
		if ran := moderator.operation != ""; ran != test.allowed {
			t.Fatalf("%s at level %d ran = %v, replies %q", test.text, test.level, ran, text.sent)
		}
		if !test.allowed && (len(text.sent) != 1 || !strings.Contains(text.sent[0], "raise it with /permission")) {
			t.Fatalf("%s at level %d replies = %q", test.text, test.level, text.sent)
		}
	}
}
