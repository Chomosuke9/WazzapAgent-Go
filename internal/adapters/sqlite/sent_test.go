package sqlite

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Chomosuke9/DiscordAgent-Go/internal/agent"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/conversation"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/maintenance"
)

func TestReplyToASentStickerIsAReplyToTheBot(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	first, err := store.Inbound().ClaimAndResolveSender(ctx, testCandidate(t, "sent-sticker-1", "15550000103"))
	if err != nil {
		t.Fatal(err)
	}
	key := agent.Key{TenantID: first.Message.TenantID, AccountID: first.Message.AccountID, ChatID: first.Message.ChatID}
	if err := store.Sent().RecordSentSticker(ctx, key, "BOT-STICKER-1", "wave"); err != nil {
		t.Fatal(err)
	}
	reply := testCandidate(t, "sent-sticker-2", "15550000103")
	reply.TenantID, reply.AccountID = key.TenantID, key.AccountID
	reply.ProviderQuotedMessageID = "BOT-STICKER-1"
	claimed, err := store.Inbound().ClaimAndResolveSender(ctx, reply)
	if err != nil {
		t.Fatal(err)
	}
	quote := claimed.Message.Quote
	if !claimed.Message.RepliedToBot || quote == nil || quote.Role != conversation.QuoteAssistant || quote.Text != "[sticker: wave]" {
		t.Fatalf("repliedToBot=%v quote=%#v", claimed.Message.RepliedToBot, quote)
	}
}

func TestSentStickerQuotesRespectResetAndRetention(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	first, err := store.Inbound().ClaimAndResolveSender(ctx, testCandidate(t, "old-sticker-1", "15550000104"))
	if err != nil {
		t.Fatal(err)
	}
	key := agent.Key{TenantID: first.Message.TenantID, AccountID: first.Message.AccountID, ChatID: first.Message.ChatID}
	if err := store.Sent().RecordSentSticker(ctx, key, "OLD-STICKER", "wave"); err != nil {
		t.Fatal(err)
	}
	resetAt := time.Now().Add(time.Minute).UnixMilli()
	if _, err := store.db.ExecContext(ctx, `INSERT INTO history_resets(tenant_id, account_id, chat_id, cutoff_sequence, config_version, reset_at_ms)
	  VALUES (?, ?, ?, 0, 1, ?)`, key.TenantID.String(), key.AccountID.String(), key.ChatID.String(), resetAt); err != nil {
		t.Fatal(err)
	}
	reply := testCandidate(t, "old-sticker-2", "15550000104")
	reply.TenantID, reply.AccountID = key.TenantID, key.AccountID
	reply.ProviderQuotedMessageID = "OLD-STICKER"
	claimed, err := store.Inbound().ClaimAndResolveSender(ctx, reply)
	if err != nil {
		t.Fatal(err)
	}
	if quote := claimed.Message.Quote; quote == nil || quote.Role != conversation.QuoteAssistant || strings.Contains(quote.Text, "wave") {
		t.Fatalf("pre-reset sticker quote = %#v", quote)
	}

	now := time.Now().Add(2 * time.Hour)
	if _, err := store.Maintain(ctx, maintenance.Request{TenantID: key.TenantID, Now: now, DeleteBefore: now.Add(-time.Hour), BatchSize: 10}); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sent_stickers WHERE tenant_id = ?`, key.TenantID.String()).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("sent stickers after maintenance = %d, err=%v", remaining, err)
	}
}

func TestReplyToALaterPartOfALongReplyIsAReplyToTheBot(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	first, err := store.Inbound().ClaimAndResolveSender(ctx, testCandidate(t, "long-reply-1", "15550000105"))
	if err != nil {
		t.Fatal(err)
	}
	key := agent.Key{TenantID: first.Message.TenantID, AccountID: first.Message.AccountID, ChatID: first.Message.ChatID}
	if err := store.Sent().RecordSentSticker(ctx, key, "PART-1", "wave"); err != nil {
		t.Fatal(err)
	}
	if err := store.Sent().RecordReceiptAliases(ctx, key, "PART-1", []string{"PART-2", "PART-3", "PART-1", ""}); err != nil {
		t.Fatal(err)
	}
	reply := testCandidate(t, "long-reply-2", "15550000105")
	reply.TenantID, reply.AccountID = key.TenantID, key.AccountID
	reply.ProviderQuotedMessageID = "PART-3"
	claimed, err := store.Inbound().ClaimAndResolveSender(ctx, reply)
	if err != nil {
		t.Fatal(err)
	}
	if !claimed.Message.RepliedToBot || claimed.Message.Quote == nil || claimed.Message.Quote.Role != conversation.QuoteAssistant {
		t.Fatalf("repliedToBot=%v quote=%#v", claimed.Message.RepliedToBot, claimed.Message.Quote)
	}
	var aliases int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM receipt_aliases WHERE tenant_id = ?`, key.TenantID.String()).Scan(&aliases); err != nil || aliases != 2 {
		t.Fatalf("aliases = %d, err=%v", aliases, err)
	}
	now := time.Now().Add(2 * time.Hour)
	if _, err := store.Maintain(ctx, maintenance.Request{TenantID: key.TenantID, Now: now, DeleteBefore: now.Add(-time.Hour), BatchSize: 10}); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM receipt_aliases WHERE tenant_id = ?`, key.TenantID.String()).Scan(&aliases); err != nil || aliases != 0 {
		t.Fatalf("aliases after maintenance = %d, err=%v", aliases, err)
	}
}
