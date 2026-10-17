package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"strings"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
)

// MemoryStore is each chat's long-term memory (/memory).
type MemoryStore struct{ *Store }

func (store *Store) Memories() *MemoryStore { return &MemoryStore{Store: store} }

// memoryCodeAlphabet avoids look-alike characters, since people type codes.
const memoryCodeAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"

const memoryCodeLength = 4

func newMemoryCode() (string, error) {
	var raw [memoryCodeLength]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	code := make([]byte, memoryCodeLength)
	for index, value := range raw {
		code[index] = memoryCodeAlphabet[int(value)%len(memoryCodeAlphabet)]
	}
	return string(code), nil
}

// AddMemory saves text in the chat's memory. Saving text the chat already
// has returns that memory with added false, so a replayed command does not
// store it twice. A full memory returns an agent.ErrorResourceExhausted error.
func (store *MemoryStore) AddMemory(ctx context.Context, key agent.Key, text string) (agent.Memory, bool, error) {
	text = strings.TrimSpace(text)
	if err := key.Validate(); err != nil {
		return agent.Memory{}, false, agent.NewError(agent.ErrorInvalidArgument, "add memory", err)
	}
	if err := agent.ValidateMemoryText(text); err != nil {
		return agent.Memory{}, false, agent.NewError(agent.ErrorInvalidArgument, "add memory", err)
	}
	scope := []any{key.TenantID.String(), key.AccountID.String(), key.ChatID.String()}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return agent.Memory{}, false, storageError("begin add memory", err)
	}
	defer tx.Rollback()
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT code FROM memories WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND text = ?`,
		append(scope, text)...).Scan(&existing)
	if err == nil {
		return agent.Memory{Code: existing, Text: text}, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return agent.Memory{}, false, storageError("look up memory", err)
	}
	var count int
	var lastSeq int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MAX(seq), 0) FROM memories WHERE tenant_id = ? AND account_id = ? AND chat_id = ?`,
		scope...).Scan(&count, &lastSeq); err != nil {
		return agent.Memory{}, false, storageError("count memories", err)
	}
	if count >= agent.MaxMemories {
		return agent.Memory{}, false, agent.Errorf(agent.ErrorResourceExhausted, "add memory", "a chat keeps at most %d memories", agent.MaxMemories)
	}
	for range 8 {
		code, err := newMemoryCode()
		if err != nil {
			return agent.Memory{}, false, agent.NewError(agent.ErrorInternal, "create memory code", err)
		}
		result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO memories(tenant_id, account_id, chat_id, code, seq, text, created_at_ms)
		  VALUES (?, ?, ?, ?, ?, ?, ?)`,
			append(scope, code, lastSeq+1, text, store.clock.Now().UTC().UnixMilli())...)
		if err != nil {
			return agent.Memory{}, false, storageError("add memory", err)
		}
		if inserted, err := result.RowsAffected(); err != nil {
			return agent.Memory{}, false, storageError("add memory", err)
		} else if inserted == 0 {
			continue // the code is taken in this chat; draw another
		}
		if err := tx.Commit(); err != nil {
			return agent.Memory{}, false, storageError("commit add memory", err)
		}
		return agent.Memory{Code: code, Text: text}, true, nil
	}
	return agent.Memory{}, false, agent.NewError(agent.ErrorInternal, "create memory code", errors.New("no free memory code"))
}

// Memories lists the chat's memories, oldest first.
func (store *MemoryStore) Memories(ctx context.Context, key agent.Key) ([]agent.Memory, error) {
	if err := key.Validate(); err != nil {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "list memories", err)
	}
	rows, err := store.read.QueryContext(ctx, `SELECT code, text FROM memories WHERE tenant_id = ? AND account_id = ? AND chat_id = ? ORDER BY seq`,
		key.TenantID.String(), key.AccountID.String(), key.ChatID.String())
	if err != nil {
		return nil, storageError("list memories", err)
	}
	defer rows.Close()
	var memories []agent.Memory
	for rows.Next() {
		var memory agent.Memory
		if err := rows.Scan(&memory.Code, &memory.Text); err != nil {
			return nil, storageError("scan memory", err)
		}
		memories = append(memories, memory)
	}
	if err := rows.Err(); err != nil {
		return nil, storageError("list memories", err)
	}
	return memories, nil
}

// DeleteMemories deletes the chat's memories with these codes and returns
// the ones it deleted. Unknown codes are skipped.
func (store *MemoryStore) DeleteMemories(ctx context.Context, key agent.Key, codes []string) ([]agent.Memory, error) {
	if err := key.Validate(); err != nil {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "delete memories", err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, storageError("begin delete memories", err)
	}
	defer tx.Rollback()
	var deleted []agent.Memory
	for _, code := range codes {
		memory := agent.Memory{Code: strings.ToLower(strings.TrimSpace(code))}
		err := tx.QueryRowContext(ctx, `DELETE FROM memories WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND code = ? RETURNING text`,
			key.TenantID.String(), key.AccountID.String(), key.ChatID.String(), memory.Code).Scan(&memory.Text)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, storageError("delete memory", err)
		}
		deleted = append(deleted, memory)
	}
	if err := tx.Commit(); err != nil {
		return nil, storageError("commit delete memories", err)
	}
	return deleted, nil
}
