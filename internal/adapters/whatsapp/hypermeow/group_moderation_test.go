package hypermeow

import (
	"context"
	"testing"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
	inboundcommands "github.com/Chomosuke9/WazzapAgent-Go/internal/inbound/commands"
)

// The /group handler discovers the port by type assertion, so a drifted method
// set would only fail at runtime without this guard.
var _ inboundcommands.GroupModerator = (*Adapter)(nil)

type muteTargets struct {
	staticTargets
	ref     identity.SenderRef
	minutes uint32
	now     time.Time
}

func (targets *muteTargets) SetChatMute(_ context.Context, _ agent.Key, ref identity.SenderRef, minutes uint32, now time.Time) error {
	targets.ref, targets.minutes, targets.now = ref, minutes, now
	return nil
}

func moderationKey(t *testing.T, adapter *Adapter) agent.Key {
	t.Helper()
	chatID, _ := identity.NewChatID()
	return agent.Key{TenantID: adapter.tenantID, AccountID: adapter.accountID, ChatID: chatID}
}

func TestGroupModerationRequiresConnection(t *testing.T) {
	adapter, _ := normalizationAdapter(t)
	adapter.targets = staticTargets{chatAddress: "123456789@g.us"}
	key := moderationKey(t, adapter)
	ref, _ := identity.ParseSenderRef("abc123")
	target, _ := identity.NewMessageID()
	calls := map[string]error{
		"announce":    adapter.SetGroupAnnounce(context.Background(), key, true),
		"description": adapter.SetGroupDescription(context.Background(), key, "rules"),
		"revoke":      adapter.RevokeGroupMessage(context.Background(), key, target),
		"kick":        adapter.RemoveGroupMember(context.Background(), key, ref),
	}
	for name, err := range calls {
		if !agent.IsCode(err, agent.ErrorNotReady) {
			t.Fatalf("%s error = %v, want not ready", name, err)
		}
	}
}

func TestGroupModerationRejectsNonGroupTarget(t *testing.T) {
	adapter, _ := normalizationAdapter(t)
	adapter.targets = staticTargets{chatAddress: "15550000077@s.whatsapp.net"}
	adapter.ready.Store(true)
	ref, _ := identity.ParseSenderRef("abc123")
	err := adapter.RemoveGroupMember(context.Background(), moderationKey(t, adapter), ref)
	if !agent.IsCode(err, agent.ErrorIntegrityFailure) {
		t.Fatalf("error = %v, want integrity failure", err)
	}
}

func TestMuteGroupMemberIsLocalAndWorksOffline(t *testing.T) {
	adapter, _ := normalizationAdapter(t)
	targets := &muteTargets{}
	adapter.targets = targets
	ref, _ := identity.ParseSenderRef("abc123")
	now := time.Date(2026, 9, 16, 1, 2, 3, 0, time.UTC)
	if err := adapter.MuteGroupMember(context.Background(), moderationKey(t, adapter), ref, 15, now); err != nil {
		t.Fatalf("mute: %v", err)
	}
	if targets.ref != ref || targets.minutes != 15 || !targets.now.Equal(now) {
		t.Fatalf("mute stored %#v", targets)
	}
}
