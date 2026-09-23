package hypermeow

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
	"github.com/polymorfa/hypermeow/types"
	"github.com/polymorfa/hypermeow/types/events"
)

type recordedGroupName struct {
	tenantID  identity.TenantID
	accountID identity.AccountID
	address   string
	name      string
}

type recordingGroupNameStore struct {
	entries []recordedGroupName
}

func (store *recordingGroupNameStore) SaveGroupName(_ context.Context, tenantID identity.TenantID, accountID identity.AccountID, address, name string) error {
	store.entries = append(store.entries, recordedGroupName{tenantID, accountID, address, name})
	return nil
}

func TestGroupEventsKeepActualNameCurrent(t *testing.T) {
	adapter, _ := normalizationAdapter(t)
	tenantID, accountID := adapter.tenantID, adapter.accountID
	store := &recordingGroupNameStore{}
	group := types.NewJID("120363000000000001", types.GroupServer)
	adapter.groupNames = store
	adapter.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	adapter.handleEvent(&events.JoinedGroup{GroupInfo: types.GroupInfo{JID: group, GroupName: types.GroupName{Name: "Tim Proyek"}}})
	adapter.handleEvent(&events.GroupInfo{JID: group, Name: &types.GroupName{Name: "Tim Baru"}})
	if len(store.entries) != 2 {
		t.Fatalf("stored group names = %#v", store.entries)
	}
	for index, want := range []string{"Tim Proyek", "Tim Baru"} {
		got := store.entries[index]
		if got.tenantID != tenantID || got.accountID != accountID || got.address != group.String() || got.name != want {
			t.Fatalf("stored group name %d = %#v, want %q", index, got, want)
		}
	}
}
