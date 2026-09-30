package sqlite

import (
	"context"
	"errors"

	"github.com/Chomosuke9/DiscordAgent-Go/internal/agent"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/identity"
)

// SentStore records the stickers the bot sent and the extra parts of long
// replies, so a reply to any of them counts as a reply to the bot.
type SentStore struct{ *Store }

func (store *Store) Sent() *SentStore { return &SentStore{Store: store} }

// RecordSentSticker binds a sent sticker's provider receipt to a new
// assistant message ID. name is the sticker's name as the model chose it.
func (store *SentStore) RecordSentSticker(ctx context.Context, key agent.Key, providerReceipt, name string) error {
	if err := key.Validate(); err != nil || providerReceipt == "" {
		return agent.NewError(agent.ErrorInvalidArgument, "record sent sticker", errors.New("chat and provider receipt are required"))
	}
	messageID, err := identity.NewMessageID()
	if err != nil {
		return agent.NewError(agent.ErrorInternal, "record sent sticker", err)
	}
	_, err = store.db.ExecContext(ctx, `INSERT INTO sent_stickers(tenant_id, account_id, chat_id, provider_receipt, message_id, name, sent_at_ms)
	  VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
		key.TenantID.String(), key.AccountID.String(), key.ChatID.String(), providerReceipt, messageID.String(), name,
		store.clock.Now().UTC().UnixMilli())
	if err != nil {
		return storageError("record sent sticker", err)
	}
	return nil
}

// RecordReceiptAliases binds the receipts of a message's later parts to the
// receipt of its first part. A reply to any part then resolves like a reply
// to the first.
func (store *SentStore) RecordReceiptAliases(ctx context.Context, key agent.Key, primary string, aliases []string) error {
	if err := key.Validate(); err != nil || primary == "" {
		return agent.NewError(agent.ErrorInvalidArgument, "record receipt aliases", errors.New("chat and primary receipt are required"))
	}
	nowMS := store.clock.Now().UTC().UnixMilli()
	for _, alias := range aliases {
		if alias == "" || alias == primary {
			continue
		}
		if _, err := store.db.ExecContext(ctx, `INSERT INTO receipt_aliases(tenant_id, account_id, chat_id, alias_receipt, primary_receipt, created_at_ms)
		  VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
			key.TenantID.String(), key.AccountID.String(), key.ChatID.String(), alias, primary, nowMS); err != nil {
			return storageError("record receipt alias", err)
		}
	}
	return nil
}
