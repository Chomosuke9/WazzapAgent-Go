package sqlite

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/Chomosuke9/DiscordAgent-Go/internal/agent"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/conversation"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/identity"
)

// SaveGroupName updates the name of a server channel already known to this
// account. Channels with no stored bot history do not create a new chat row.
func (store *InboundStore) SaveGroupName(ctx context.Context, tenantID identity.TenantID, accountID identity.AccountID, address, name string) error {
	if store == nil || store.Store == nil || store.db == nil {
		return agent.NewError(agent.ErrorUnavailable, "save group name", errors.New("chat store is unavailable"))
	}
	name = strings.TrimSpace(name)
	if _, err := identity.ParseUserID(address); err != nil || tenantID.IsZero() || accountID.IsZero() ||
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
