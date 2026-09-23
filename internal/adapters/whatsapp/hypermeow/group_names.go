package hypermeow

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/polymorfa/hypermeow/types"
)

func (adapter *Adapter) requestGroupNameSync() {
	if adapter.rootCtx == nil || adapter.rootCtx.Err() != nil {
		return
	}
	select {
	case adapter.groupSync <- struct{}{}:
	default:
		// A refresh is already pending.
	}
}

func (adapter *Adapter) groupNameSyncWorker() {
	defer adapter.wait.Done()
	for {
		select {
		case <-adapter.rootCtx.Done():
			return
		case <-adapter.groupSync:
			if !adapter.refreshJoinedGroupNames() {
				timer := time.NewTimer(2 * time.Second)
				select {
				case <-adapter.rootCtx.Done():
					timer.Stop()
					return
				case <-timer.C:
					adapter.requestGroupNameSync()
				}
			}
		}
	}
}

func (adapter *Adapter) refreshJoinedGroupNames() bool {
	adapter.groupMu.RLock()
	revision := adapter.groupRevision
	adapter.groupMu.RUnlock()
	readCtx, cancel := context.WithTimeout(adapter.rootCtx, adapter.sendTimeout)
	groups, err := adapter.client.GetJoinedGroups(readCtx)
	if err != nil {
		code := agent.CodeOf(nativeEffectError(readCtx, "read joined groups", err))
		cancel()
		if adapter.rootCtx.Err() == nil {
			adapter.logger.Warn("WhatsApp group names could not be refreshed", "code", code)
		}
		return false
	}
	cancel()
	snapshot := make(map[types.JID]cachedGroup, len(groups))
	encoded := make(map[string][]byte, len(groups))
	observedAt := time.Now().UTC().UnixMilli()
	for _, group := range groups {
		if group != nil && group.JID.Server == types.GroupServer {
			payload, marshalErr := json.Marshal(group)
			if marshalErr != nil {
				return false
			}
			encoded[group.JID.ToNonAD().String()] = payload
			snapshot[group.JID.ToNonAD()] = cachedGroup{info: copyGroupInfo(*group), observedAt: observedAt}
		}
	}
	updated, failed := 0, 0
	for _, group := range groups {
		if adapter.rootCtx.Err() != nil {
			return false
		}
		if adapter.groupNames == nil || group == nil || group.JID.Server != types.GroupServer || strings.TrimSpace(group.Name) == "" {
			continue
		}
		if err := adapter.groupNames.SaveGroupName(adapter.rootCtx, adapter.tenantID, adapter.accountID, group.JID.ToNonAD().String(), group.Name); err != nil {
			failed++
			continue
		}
		updated++
	}
	adapter.groupMu.Lock()
	if adapter.groupRevision != revision || !adapter.ready.Load() {
		adapter.groupMu.Unlock()
		return false
	}
	if err := adapter.groupMetadata.ReplaceGroupMetadata(adapter.rootCtx, adapter.tenantID, adapter.accountID, encoded, observedAt); err != nil {
		adapter.groupMu.Unlock()
		adapter.logger.Warn("WhatsApp group metadata could not be stored", "code", agent.CodeOf(err))
		return false
	}
	adapter.groups = snapshot
	adapter.groupsReady = true
	adapter.groupMu.Unlock()
	if failed > 0 {
		adapter.logger.Warn("Some WhatsApp group names could not be stored", "groups", failed)
	}
	adapter.logger.Info("WhatsApp group names refreshed", "groups", updated)
	return true
}

func (adapter *Adapter) cacheGroupName(ctx context.Context, group types.JID, name string) {
	if adapter.groupNames == nil || group.Server != types.GroupServer || strings.TrimSpace(name) == "" {
		return
	}
	if err := adapter.groupNames.SaveGroupName(ctx, adapter.tenantID, adapter.accountID, group.ToNonAD().String(), name); err != nil && ctx.Err() == nil {
		adapter.logger.Warn("WhatsApp group name could not be stored", "code", agent.CodeOf(err))
	}
}
