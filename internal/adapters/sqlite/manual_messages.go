package sqlite

import (
	"context"
	"errors"
	"strings"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/effect"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

// RecordManualAssistantMessage stores a successfully sent local UI message in
// the normal transcript and binds its provider receipt for later revocation.
func (store *Store) RecordManualAssistantMessage(ctx context.Context, key agent.Key, entry agent.HistoryEntry, providerReceipt string) error {
	if err := key.Validate(); err != nil || entry.Role != agent.HistoryAssistant ||
		entry.Delivery != agent.DeliverySucceeded || entry.Causation.Kind != agent.CausationRequest ||
		strings.TrimSpace(providerReceipt) == "" {
		return agent.NewError(agent.ErrorInvalidArgument, "record manual WhatsApp message", errors.New("valid sent assistant message and provider receipt are required"))
	}
	if _, err := agent.DigestHistoryEntry(entry); err != nil {
		return err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return storageError("begin manual message record", err)
	}
	defer tx.Rollback()
	if err := store.appendHistoryEntryTx(ctx, tx, key, entry, entry.CreatedAt.UTC().UnixMilli()); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO manual_message_targets(
	    tenant_id, account_id, chat_id, message_id, provider_receipt, created_at_ms
	) VALUES (?, ?, ?, ?, ?, ?)`,
		key.TenantID.String(), key.AccountID.String(), key.ChatID.String(), entry.MessageID.String(), providerReceipt, entry.CreatedAt.UTC().UnixMilli(),
	)
	if err != nil {
		if isUniqueConstraint(err) {
			return agent.NewError(agent.ErrorConflict, "record manual WhatsApp message", errors.New("message receipt is already bound"))
		}
		return storageError("record manual message target", err)
	}
	if err := tx.Commit(); err != nil {
		return storageError("commit manual message record", err)
	}
	return nil
}

func (store *Store) IsMessageDeleted(ctx context.Context, key agent.Key, messageID identity.MessageID) (bool, error) {
	if err := key.Validate(); err != nil || messageID.IsZero() {
		return false, agent.NewError(agent.ErrorInvalidArgument, "read WhatsApp message deletion state", errors.New("valid chat and message IDs are required"))
	}
	var deleted int
	err := store.db.QueryRowContext(ctx, `SELECT EXISTS(
	    SELECT 1 FROM typed_effects
	    WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND target_message_id = ?
	      AND effect_kind = ? AND state = ?
	)`,
		key.TenantID.String(), key.AccountID.String(), key.ChatID.String(), messageID.String(),
		uint8(effect.KindDeleteMessage), uint8(effect.StateSucceeded),
	).Scan(&deleted)
	if err != nil {
		return false, storageError("read WhatsApp message deletion state", err)
	}
	return deleted == 1, nil
}
