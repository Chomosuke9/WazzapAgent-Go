package sqlite

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

// SaveGroupName updates metadata for a group already known to this account.
// Groups with no stored bot history do not create a new chat row.
func (store *InboundStore) SaveGroupName(ctx context.Context, tenantID identity.TenantID, accountID identity.AccountID, address, name string) error {
	if store == nil || store.Store == nil || store.db == nil {
		return agent.NewError(agent.ErrorUnavailable, "save group name", errors.New("chat store is unavailable"))
	}
	name = strings.TrimSpace(name)
	if tenantID.IsZero() || accountID.IsZero() || !strings.HasSuffix(address, "@g.us") || len(address) > 512 ||
		name == "" || !utf8.ValidString(name) || len(name) > 512 {
		return agent.NewError(agent.ErrorInvalidArgument, "save group name", errors.New("group metadata is invalid"))
	}
	_, err := store.db.ExecContext(ctx, `UPDATE chats SET group_name = ?
		WHERE tenant_id = ? AND account_id = ? AND provider_address = ? AND kind = ? AND group_name <> ?`,
		name, tenantID.String(), accountID.String(), address, uint8(conversation.ChatGroup), name)
	if err != nil {
		return storageError("save group name", err)
	}
	return nil
}
