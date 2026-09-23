package hypermeow

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/polymorfa/hypermeow/types"
	"github.com/polymorfa/hypermeow/types/events"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/policy"
)

func TestGroupMetadataReadsAndEventsUseCache(t *testing.T) {
	adapter, bot := normalizationAdapter(t)
	chat := types.NewJID("120363000000000001", types.GroupServer)
	member := types.NewJID("10000000001", types.HiddenUserServer)
	chatID, _ := identity.NewChatID()
	key := agent.Key{TenantID: adapter.tenantID, AccountID: adapter.accountID, ChatID: chatID}
	adapter.targets = staticTargets{chatAddress: chat.String()}
	adapter.ready.Store(true)
	seedGroupCache(t, adapter, types.GroupInfo{
		JID: chat, GroupName: types.GroupName{Name: "First"}, GroupTopic: types.GroupTopic{Topic: "Old topic"},
		Participants: []types.GroupParticipant{{JID: bot, IsAdmin: true}, {JID: member}},
	})
	first, err := adapter.ReadChatContext(context.Background(), key)
	if err != nil || first.Name != "First" || first.Description != "Old topic" || !first.BotIsAdmin {
		t.Fatalf("initial cached context = %#v, err = %v", first, err)
	}
	principal, err := policy.SystemPrincipal(key)
	if err != nil {
		t.Fatal(err)
	}
	initialAuthority, err := adapter.ReadChatAuthority(context.Background(), principal)
	if err != nil || !initialAuthority.BotIsAdmin {
		t.Fatalf("initial cached authority = %#v, err = %v", initialAuthority, err)
	}
	adapter.handleEvent(&events.GroupInfo{
		JID: chat, Name: &types.GroupName{Name: "Renamed"}, Topic: &types.GroupTopic{Topic: "New topic"}, Demote: []types.JID{bot},
	})
	updated, err := adapter.ReadChatContext(context.Background(), key)
	if err != nil || updated.Name != "Renamed" || updated.Description != "New topic" || updated.BotIsAdmin {
		t.Fatalf("updated cached context = %#v, err = %v", updated, err)
	}
	updatedAuthority, err := adapter.ReadChatAuthority(context.Background(), principal)
	if err != nil || updatedAuthority.BotIsAdmin {
		t.Fatalf("updated cached authority = %#v, err = %v", updatedAuthority, err)
	}
	adapter.clearGroupCache()
	if _, err := adapter.ReadChatContext(context.Background(), key); !agent.IsCode(err, agent.ErrorNotReady) {
		t.Fatalf("unsynchronized group read = %v, want not ready", err)
	}
}

func TestGroupCacheInvalidatesOnParticipantVersionGap(t *testing.T) {
	adapter, _ := normalizationAdapter(t)
	chat := types.NewJID("120363000000000001", types.GroupServer)
	seedGroupCache(t, adapter, types.GroupInfo{JID: chat, ParticipantVersionID: "v1"})
	if !adapter.applyGroupChange(&events.GroupInfo{JID: chat, PrevParticipantVersionID: "older", ParticipantVersionID: "v3"}) {
		t.Fatal("participant version gap did not request synchronization")
	}
	if _, err := adapter.readGroupInfo(context.Background(), chat); !agent.IsCode(err, agent.ErrorNotReady) {
		t.Fatalf("read after version gap = %v, want not ready", err)
	}
}

func seedGroupCache(t *testing.T, adapter *Adapter, info types.GroupInfo) {
	t.Helper()
	adapter.cacheJoinedGroup(info)
	payload, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.groupMetadata.ReplaceGroupMetadata(context.Background(), adapter.tenantID, adapter.accountID,
		map[string][]byte{info.JID.String(): payload}, 1); err != nil {
		t.Fatal(err)
	}
	adapter.groupMu.Lock()
	adapter.groupsReady = true
	adapter.groupMu.Unlock()
}
