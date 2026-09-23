package hypermeow

import (
	"context"
	"strings"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/polymorfa/hypermeow/types"
)

func (adapter *Adapter) requestGroupNameSync() {
	if adapter.groupNames == nil || adapter.rootCtx == nil || adapter.rootCtx.Err() != nil {
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
			adapter.refreshJoinedGroupNames()
		}
	}
}

func (adapter *Adapter) refreshJoinedGroupNames() {
	readCtx, cancel := context.WithTimeout(adapter.rootCtx, adapter.sendTimeout)
	groups, err := adapter.client.GetJoinedGroups(readCtx)
	if err != nil {
		code := agent.CodeOf(nativeEffectError(readCtx, "read joined groups", err))
		cancel()
		if adapter.rootCtx.Err() == nil {
			adapter.logger.Warn("WhatsApp group names could not be refreshed", "code", code)
		}
		return
	}
	cancel()
	updated, failed := 0, 0
	for _, group := range groups {
		if adapter.rootCtx.Err() != nil {
			return
		}
		if group == nil || group.JID.Server != types.GroupServer || strings.TrimSpace(group.Name) == "" {
			continue
		}
		if err := adapter.groupNames.SaveGroupName(adapter.rootCtx, adapter.tenantID, adapter.accountID, group.JID.ToNonAD().String(), group.Name); err != nil {
			failed++
			continue
		}
		updated++
	}
	if failed > 0 {
		adapter.logger.Warn("Some WhatsApp group names could not be stored", "groups", failed)
	}
	adapter.logger.Info("WhatsApp group names refreshed", "groups", updated)
}

func (adapter *Adapter) cacheGroupName(ctx context.Context, group types.JID, name string) {
	if adapter.groupNames == nil || group.Server != types.GroupServer || strings.TrimSpace(name) == "" {
		return
	}
	if err := adapter.groupNames.SaveGroupName(ctx, adapter.tenantID, adapter.accountID, group.ToNonAD().String(), name); err != nil && ctx.Err() == nil {
		adapter.logger.Warn("WhatsApp group name could not be stored", "code", agent.CodeOf(err))
	}
}
