package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/sticker"
)

// StickerStore is the per-chat sticker catalog.
type StickerStore struct{ *Store }

func (store *Store) Stickers() *StickerStore { return &StickerStore{Store: store} }

var _ sticker.Catalog = (*StickerStore)(nil)

func (store *StickerStore) SaveSticker(ctx context.Context, key agent.Key, value sticker.Sticker) (bool, error) {
	if err := key.Validate(); err != nil || !sticker.ValidName(value.Name) || (len(value.WebP) == 0) == (len(value.Lottie) == 0) {
		return false, agent.NewError(agent.ErrorInvalidArgument, "save sticker", errors.New("chat, valid name, and exactly one of WebP or Lottie are required"))
	}
	var webp, lottie any
	if len(value.WebP) > 0 {
		webp = value.WebP
	} else {
		lottie = value.Lottie
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return false, storageError("begin save sticker", err)
	}
	defer tx.Rollback()
	var existing int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM stickers WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND name = ?`,
		key.TenantID.String(), key.AccountID.String(), key.ChatID.String(), value.Name).Scan(&existing)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, storageError("look up sticker", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO stickers(tenant_id, account_id, chat_id, name, webp, animated, lottie_json, created_at_ms)
	  VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	  ON CONFLICT(tenant_id, account_id, chat_id, name) DO UPDATE SET
	    webp = excluded.webp, animated = excluded.animated, lottie_json = excluded.lottie_json, created_at_ms = excluded.created_at_ms`,
		key.TenantID.String(), key.AccountID.String(), key.ChatID.String(), value.Name,
		webp, boolInt(value.Animated), lottie, store.clock.Now().UTC().UnixMilli())
	if err != nil {
		return false, storageError("save sticker", err)
	}
	if err := tx.Commit(); err != nil {
		return false, storageError("commit save sticker", err)
	}
	return existing == 1, nil
}

func (store *StickerStore) DeleteSticker(ctx context.Context, key agent.Key, name string) (bool, error) {
	if err := key.Validate(); err != nil {
		return false, agent.NewError(agent.ErrorInvalidArgument, "delete sticker", err)
	}
	result, err := store.db.ExecContext(ctx, `DELETE FROM stickers WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND name = ?`,
		key.TenantID.String(), key.AccountID.String(), key.ChatID.String(), name)
	if err != nil {
		return false, storageError("delete sticker", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return false, storageError("delete sticker", err)
	}
	return deleted > 0, nil
}

func (store *StickerStore) StickerNames(ctx context.Context, key agent.Key) ([]string, error) {
	if err := key.Validate(); err != nil {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "list stickers", err)
	}
	rows, err := store.read.QueryContext(ctx, `SELECT name FROM stickers WHERE tenant_id = ? AND account_id = ? AND chat_id = ? ORDER BY name`,
		key.TenantID.String(), key.AccountID.String(), key.ChatID.String())
	if err != nil {
		return nil, storageError("list stickers", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, storageError("scan sticker name", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return nil, storageError("list stickers", err)
	}
	return names, nil
}

func (store *StickerStore) LoadSticker(ctx context.Context, key agent.Key, name string) (sticker.Sticker, error) {
	if err := key.Validate(); err != nil {
		return sticker.Sticker{}, agent.NewError(agent.ErrorInvalidArgument, "load sticker", err)
	}
	value := sticker.Sticker{Name: name}
	var animated int
	err := store.read.QueryRowContext(ctx, `SELECT webp, animated, lottie_json FROM stickers
	  WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND name = ?`,
		key.TenantID.String(), key.AccountID.String(), key.ChatID.String(), name).Scan(&value.WebP, &animated, &value.Lottie)
	if errors.Is(err, sql.ErrNoRows) {
		return sticker.Sticker{}, agent.NewError(agent.ErrorNotFound, "load sticker", errors.New("no sticker has that name in this chat"))
	}
	if err != nil {
		return sticker.Sticker{}, storageError("load sticker", err)
	}
	value.Animated = animated == 1
	return value, nil
}

// RecordSentSticker binds a sent sticker's provider receipt to a new
// assistant message ID. name is empty for a sticker that is not in the catalog.
func (store *StickerStore) RecordSentSticker(ctx context.Context, key agent.Key, providerReceipt, name string) error {
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

// ReadCommandMedia returns the media payload captured with a slash command,
// or an agent.ErrorNotFound error when the command carried none.
func (store *InboundStore) ReadCommandMedia(ctx context.Context, message conversation.IncomingMessage) ([]byte, error) {
	if message.TenantID.IsZero() || message.AccountID.IsZero() || message.ChatID.IsZero() || message.InvocationID.IsZero() {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "read command media", errors.New("valid inbound message identity is required"))
	}
	var payload []byte
	err := store.read.QueryRowContext(ctx, `SELECT message_json FROM command_media
	  WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND invocation_id = ?`,
		message.TenantID.String(), message.AccountID.String(), message.ChatID.String(), message.InvocationID.String()).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, agent.NewError(agent.ErrorNotFound, "read command media", err)
	}
	if err != nil {
		return nil, storageError("read command media", err)
	}
	return payload, nil
}
