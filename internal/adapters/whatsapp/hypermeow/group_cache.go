package hypermeow

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/polymorfa/hypermeow/types"
	"github.com/polymorfa/hypermeow/types/events"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
)

type cachedGroup struct {
	info       types.GroupInfo
	observedAt int64
}

func copyGroupInfo(info types.GroupInfo) types.GroupInfo {
	info.Participants = append([]types.GroupParticipant(nil), info.Participants...)
	return info
}

func (adapter *Adapter) clearGroupCache() {
	adapter.groupMu.Lock()
	adapter.groups = make(map[types.JID]cachedGroup)
	adapter.groupsReady = false
	adapter.groupRevision++
	if adapter.groupMetadata != nil {
		if err := adapter.groupMetadata.InvalidateGroupMetadata(adapter.groupStoreContext(), adapter.tenantID, adapter.accountID); err != nil && adapter.logger != nil {
			adapter.logger.Error("group metadata could not be invalidated", "code", agent.CodeOf(err))
		}
	}
	adapter.groupMu.Unlock()
	adapter.clearMemberHandles()
	adapter.clearBroadcastHandles()
}

func (adapter *Adapter) groupStoreContext() context.Context {
	if adapter.rootCtx != nil {
		return adapter.rootCtx
	}
	return context.Background()
}

func (adapter *Adapter) readGroupInfo(ctx context.Context, chat types.JID) (types.GroupInfo, error) {
	info, _, err := adapter.readGroupSnapshot(ctx, chat)
	return info, err
}

func (adapter *Adapter) readGroupSnapshot(ctx context.Context, chat types.JID) (types.GroupInfo, int64, error) {
	adapter.groupMu.RLock()
	ready := adapter.groupsReady
	if !ready {
		adapter.groupMu.RUnlock()
		return types.GroupInfo{}, 0, agent.NewError(agent.ErrorNotReady, "read cached WhatsApp group", errors.New("group metadata is synchronizing"))
	}
	if adapter.groupMetadata == nil {
		adapter.groupMu.RUnlock()
		return types.GroupInfo{}, 0, agent.NewError(agent.ErrorNotReady, "read cached WhatsApp group", errors.New("group metadata store is unavailable"))
	}
	payload, observedAt, err := adapter.groupMetadata.LoadGroupMetadata(ctx, adapter.tenantID, adapter.accountID, chat.ToNonAD().String())
	adapter.groupMu.RUnlock()
	if err != nil {
		return types.GroupInfo{}, 0, err
	}
	var info types.GroupInfo
	if err := json.Unmarshal(payload, &info); err != nil || info.JID.ToNonAD() != chat.ToNonAD() {
		return types.GroupInfo{}, 0, agent.NewError(agent.ErrorIntegrityFailure, "decode cached WhatsApp group", errors.New("stored group metadata is invalid"))
	}
	return info, observedAt, nil
}

func (adapter *Adapter) cacheJoinedGroup(info types.GroupInfo) {
	if info.JID.Server != types.GroupServer {
		return
	}
	adapter.groupMu.Lock()
	if adapter.groups == nil {
		adapter.groups = make(map[types.JID]cachedGroup)
	}
	observedAt := time.Now().UTC().UnixMilli()
	encoded, err := json.Marshal(info)
	if err != nil || adapter.groupMetadata == nil {
		adapter.groupsReady = false
		adapter.groupRevision++
		adapter.groupMu.Unlock()
		adapter.requestGroupNameSync()
		return
	}
	if err := adapter.groupMetadata.UpsertGroupMetadata(adapter.groupStoreContext(), adapter.tenantID, adapter.accountID, info.JID.ToNonAD().String(), encoded, observedAt); err != nil {
		adapter.groupsReady = false
		adapter.groupRevision++
		adapter.groupMu.Unlock()
		adapter.requestGroupNameSync()
		return
	}
	adapter.groups[info.JID.ToNonAD()] = cachedGroup{info: copyGroupInfo(info), observedAt: observedAt}
	adapter.groupRevision++
	adapter.groupMu.Unlock()
}

// applyGroupChange patches only fields supplied by the event. If a change
// cannot be reconciled with a known snapshot, the next full sync rebuilds it.
func (adapter *Adapter) applyGroupChange(change *events.GroupInfo) bool {
	if change == nil || change.JID.Server != types.GroupServer {
		return false
	}
	chat := change.JID.ToNonAD()
	adapter.groupMu.Lock()
	defer adapter.groupMu.Unlock()
	entry, exists := adapter.groups[chat]
	info := entry.info
	if !exists || len(change.UnknownChanges) > 0 ||
		(change.PrevParticipantVersionID != "" && info.ParticipantVersionID != "" && change.PrevParticipantVersionID != info.ParticipantVersionID) {
		adapter.groupsReady = false
		adapter.groupRevision++
		_ = adapter.groupMetadata.InvalidateGroupMetadata(adapter.groupStoreContext(), adapter.tenantID, adapter.accountID)
		return true
	}
	if change.Delete != nil {
		if err := adapter.groupMetadata.DeleteGroupMetadata(adapter.groupStoreContext(), adapter.tenantID, adapter.accountID, chat.String()); err != nil {
			adapter.groupsReady = false
			adapter.groupRevision++
			return true
		}
		delete(adapter.groups, chat)
		adapter.groupRevision++
		return false
	}
	if change.Name != nil {
		info.GroupName = *change.Name
	}
	if change.Topic != nil {
		info.GroupTopic = *change.Topic
	}
	for _, jid := range change.Leave {
		for index, participant := range info.Participants {
			if participantMatches(participant, jid) {
				info.Participants = append(info.Participants[:index], info.Participants[index+1:]...)
				break
			}
		}
	}
	for _, jid := range change.Join {
		found := false
		for _, participant := range info.Participants {
			if participantMatches(participant, jid) {
				found = true
				break
			}
		}
		if !found {
			info.Participants = append(info.Participants, types.GroupParticipant{JID: jid.ToNonAD()})
		}
	}
	for _, update := range []struct {
		addresses []types.JID
		admin     bool
	}{
		{change.Promote, true}, {change.Demote, false},
	} {
		for _, jid := range update.addresses {
			found := false
			for index := range info.Participants {
				if participantMatches(info.Participants[index], jid) {
					info.Participants[index].IsAdmin = update.admin
					info.Participants[index].IsSuperAdmin = false
					found = true
					break
				}
			}
			if !found {
				adapter.groupsReady = false
				adapter.groupRevision++
				_ = adapter.groupMetadata.InvalidateGroupMetadata(adapter.groupStoreContext(), adapter.tenantID, adapter.accountID)
				return true
			}
		}
	}
	if change.ParticipantVersionID != "" {
		info.ParticipantVersionID = change.ParticipantVersionID
	}
	info.ParticipantCount = len(info.Participants)
	observedAt := time.Now().UTC().UnixMilli()
	encoded, err := json.Marshal(info)
	if err != nil || adapter.groupMetadata == nil || adapter.groupMetadata.UpsertGroupMetadata(adapter.groupStoreContext(), adapter.tenantID, adapter.accountID, chat.String(), encoded, observedAt) != nil {
		adapter.groupsReady = false
		adapter.groupRevision++
		return true
	}
	adapter.groups[chat] = cachedGroup{info: info, observedAt: observedAt}
	adapter.groupRevision++
	return false
}
