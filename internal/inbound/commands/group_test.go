package commands

import (
	"context"
	"testing"
	"time"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/types"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

type recordingCommandAdapter struct {
	client  *recordingCommandClient
	targets *recordingGroupTargets
}

func (adapter *recordingCommandAdapter) CommandClient() WhatsAppCommandClient { return adapter.client }
func (adapter *recordingCommandAdapter) CommandTargets() GroupTargetStore     { return adapter.targets }

type recordingCommandClient struct {
	operation   string
	closed      bool
	description string
}

func (client *recordingCommandClient) SetGroupAnnounce(_ context.Context, _ types.JID, closed bool) error {
	client.operation, client.closed = "announce", closed
	return nil
}
func (client *recordingCommandClient) SetGroupDescription(_ context.Context, _ types.JID, value string) error {
	client.operation, client.description = "description", value
	return nil
}
func (client *recordingCommandClient) BuildRevoke(types.JID, types.JID, types.MessageID) *waE2E.Message {
	return &waE2E.Message{}
}
func (client *recordingCommandClient) SendMessage(context.Context, types.JID, *waE2E.Message, ...whatsmeow.SendRequestExtra) (whatsmeow.SendResponse, error) {
	client.operation = "delete"
	return whatsmeow.SendResponse{}, nil
}
func (client *recordingCommandClient) UpdateGroupParticipants(context.Context, types.JID, []types.JID, whatsmeow.ParticipantChange) ([]types.GroupParticipant, error) {
	client.operation = "kick"
	return []types.GroupParticipant{{}}, nil
}

type recordingGroupTargets struct {
	operation string
	ref       identity.SenderRef
	duration  uint32
	now       time.Time
}

func (*recordingGroupTargets) ResolveChatAddress(context.Context, agent.Key) (string, error) {
	return "123456789@g.us", nil
}
func (*recordingGroupTargets) ResolveMessageTarget(context.Context, agent.Key, identity.MessageID) (string, string, string, time.Time, error) {
	return "123456789@g.us", "provider-message", "10000000001@lid", time.Now(), nil
}
func (*recordingGroupTargets) ResolveLID(context.Context, agent.Key, identity.SenderRef) (identity.LID, error) {
	return identity.ParseLID("10000000001@lid")
}
func (targets *recordingGroupTargets) SetChatMute(_ context.Context, _ agent.Key, ref identity.SenderRef, duration uint32, now time.Time) error {
	targets.operation, targets.ref, targets.duration, targets.now = "mute", ref, duration, now
	return nil
}

func TestHandleGroupOwnsEverySupportedSubcommand(t *testing.T) {
	key := groupCommandKey(t)
	target, _ := identity.NewMessageID()
	now := time.Date(2026, 9, 16, 1, 2, 3, 0, time.UTC)
	tests := []struct {
		name, command, operation string
		target                   identity.MessageID
		check                    func(*testing.T, *recordingCommandAdapter)
	}{
		{name: "close", command: "/group close", operation: "announce", check: func(t *testing.T, got *recordingCommandAdapter) {
			if !got.client.closed {
				t.Fatal("close did not enable announce mode")
			}
		}},
		{name: "open", command: "/group open", operation: "announce", check: func(t *testing.T, got *recordingCommandAdapter) {
			if got.client.closed {
				t.Fatal("open enabled announce mode")
			}
		}},
		{name: "description", command: "/group description Aturan baru", operation: "description", check: func(t *testing.T, got *recordingCommandAdapter) {
			if got.client.description != "Aturan baru" {
				t.Fatalf("description = %q", got.client.description)
			}
		}},
		{name: "delete", command: "/group delete", target: target, operation: "delete"},
		{name: "mute", command: "/group mute @Alice (abc123) 15", operation: "mute", check: func(t *testing.T, got *recordingCommandAdapter) {
			if got.targets.ref.String() != "abc123" || got.targets.duration != 15 || !got.targets.now.Equal(now) {
				t.Fatal("wrong mute arguments")
			}
		}},
		{name: "kick", command: "/group kick @Alice Smith (abc123)", operation: "kick"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter := &recordingCommandAdapter{client: &recordingCommandClient{}, targets: &recordingGroupTargets{}}
			if err := HandleGroup(context.Background(), adapter, key, test.command, test.target, now); err != nil {
				t.Fatalf("handle command: %v", err)
			}
			operation := adapter.client.operation
			if test.operation == "mute" {
				operation = adapter.targets.operation
			}
			if operation != test.operation {
				t.Fatalf("operation = %q, want %q", operation, test.operation)
			}
			if test.check != nil {
				test.check(t, adapter)
			}
		})
	}
}

func TestHandleGroupRejectsMalformedCommandsBeforeProviderCall(t *testing.T) {
	key := groupCommandKey(t)
	for _, value := range []string{"/group close now", "/group description", "/group delete", "/group mute @Alice (abc123) 43201", "/group kick @Alice (invalid)", "/group admin"} {
		adapter := &recordingCommandAdapter{client: &recordingCommandClient{}, targets: &recordingGroupTargets{}}
		if err := HandleGroup(context.Background(), adapter, key, value, identity.MessageID{}, time.Now().UTC()); err == nil {
			t.Fatalf("malformed command accepted: %q", value)
		}
		if adapter.client.operation != "" || adapter.targets.operation != "" {
			t.Fatalf("provider called for malformed command %q", value)
		}
	}
}

func groupCommandKey(t *testing.T) agent.Key {
	t.Helper()
	tenantID, _ := identity.NewTenantID()
	accountID, _ := identity.NewAccountID()
	chatID, _ := identity.NewChatID()
	return agent.Key{TenantID: tenantID, AccountID: accountID, ChatID: chatID}
}
