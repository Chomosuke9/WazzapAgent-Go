package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

const senderRefMigrationAttempts = 128

type senderRefScope struct {
	tenantID  string
	accountID string
	chatID    string
}

type senderRefRewrite struct {
	scope         senderRefScope
	participantID string
	old           string
	new           string
}

// normalizeLegacySenderRefs upgrades references emitted by the previous Go
// format (u_ plus eight Crockford characters) before any store caller can read
// them. The rewrite is transactional and updates every durable identity
// reference, including pending typed group commands, so existing data keeps
// resolving after the canonical format changes to six base36 characters.
func normalizeLegacySenderRefs(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `SELECT tenant_id, account_id, chat_id, participant_id, sender_ref
        FROM sender_refs ORDER BY tenant_id, account_id, chat_id, participant_id`)
	if err != nil {
		return agent.NewError(agent.ErrorStorageFailure, "inspect sender ref format", err)
	}

	occupied := make(map[senderRefScope]map[string]struct{})
	legacy := make([]senderRefRewrite, 0)
	for rows.Next() {
		var scope senderRefScope
		var participantID, value string
		if err := rows.Scan(&scope.tenantID, &scope.accountID, &scope.chatID, &participantID, &value); err != nil {
			_ = rows.Close()
			return agent.NewError(agent.ErrorStorageFailure, "scan sender ref format", err)
		}
		refs := occupied[scope]
		if refs == nil {
			refs = make(map[string]struct{})
			occupied[scope] = refs
		}
		if _, parseErr := identity.ParseSenderRef(value); parseErr == nil {
			refs[value] = struct{}{}
			continue
		}
		legacy = append(legacy, senderRefRewrite{scope: scope, participantID: participantID, old: value})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return agent.NewError(agent.ErrorStorageFailure, "iterate sender ref format", err)
	}
	if err := rows.Close(); err != nil {
		return agent.NewError(agent.ErrorStorageFailure, "close sender ref format scan", err)
	}
	if len(legacy) == 0 {
		return nil
	}

	for index := range legacy {
		rewrite := &legacy[index]
		refs := occupied[rewrite.scope]
		for attempt := 0; attempt < senderRefMigrationAttempts; attempt++ {
			candidate, err := identity.NewSenderRef()
			if err != nil {
				return agent.NewError(agent.ErrorInternal, "generate migrated sender ref", err)
			}
			if _, exists := refs[candidate.String()]; exists {
				continue
			}
			rewrite.new = candidate.String()
			refs[rewrite.new] = struct{}{}
			break
		}
		if rewrite.new == "" {
			return agent.NewError(agent.ErrorResourceExhausted, "migrate sender ref format", fmt.Errorf("collision retry limit reached"))
		}
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return agent.NewError(agent.ErrorStorageFailure, "begin sender ref migration", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "PRAGMA defer_foreign_keys = ON"); err != nil {
		return agent.NewError(agent.ErrorStorageFailure, "defer sender ref migration constraints", err)
	}
	for _, rewrite := range legacy {
		args := []any{rewrite.new, rewrite.scope.tenantID, rewrite.scope.accountID, rewrite.scope.chatID, rewrite.old}
		statements := []string{
			`UPDATE inbound_events SET sender_ref = ? WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND sender_ref = ?`,
			`UPDATE inbound_events SET quoted_sender_ref = ? WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND quoted_sender_ref = ?`,
			`UPDATE history_entries SET sender_ref = ? WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND sender_ref = ?`,
			`UPDATE history_entries SET quoted_sender_ref = ? WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND quoted_sender_ref = ?`,
			`UPDATE chat_mutes SET sender_ref = ? WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND sender_ref = ?`,
		}
		for _, statement := range statements {
			if _, err := tx.ExecContext(ctx, statement, args...); err != nil {
				return agent.NewError(agent.ErrorStorageFailure, "rewrite sender ref references", err)
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE typed_effects
            SET command_text = REPLACE(command_text, ?, ?)
            WHERE tenant_id = ? AND account_id = ? AND chat_id = ?`,
			rewrite.old, rewrite.new, rewrite.scope.tenantID, rewrite.scope.accountID, rewrite.scope.chatID,
		); err != nil {
			return agent.NewError(agent.ErrorStorageFailure, "rewrite sender ref commands", err)
		}
		result, err := tx.ExecContext(ctx, `UPDATE sender_refs SET sender_ref = ?
            WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND participant_id = ? AND sender_ref = ?`,
			rewrite.new, rewrite.scope.tenantID, rewrite.scope.accountID, rewrite.scope.chatID, rewrite.participantID, rewrite.old,
		)
		if err != nil {
			return agent.NewError(agent.ErrorStorageFailure, "rewrite sender ref mapping", err)
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return agent.NewError(agent.ErrorStorageFailure, "inspect sender ref mapping", err)
		}
		if changed != 1 {
			return agent.NewError(agent.ErrorIntegrityFailure, "rewrite sender ref mapping", fmt.Errorf("mapping disappeared during migration"))
		}
	}
	if err := tx.Commit(); err != nil {
		return agent.NewError(agent.ErrorStorageFailure, "commit sender ref migration", err)
	}
	return nil
}
