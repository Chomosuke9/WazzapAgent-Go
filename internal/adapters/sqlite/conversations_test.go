package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/control"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

func TestConversationReaderListsBotTranscriptWithoutOpeningDatabaseForWrites(t *testing.T) {
	ctx := context.Background()
	dataRoot := t.TempDir()
	tenantID, _ := identity.NewTenantID()
	accountID, _ := identity.NewAccountID()
	path := filepath.Join(dataRoot, "tenants", tenantID.String(), "app.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open app database: %v", err)
	}
	defer store.Close()

	now := time.Now().UTC().Truncate(time.Millisecond)
	candidate := testCandidate(t, "conversation-reader-message", "120363000000000001@s.whatsapp.net")
	candidate.TenantID, candidate.AccountID = tenantID, accountID
	candidate.ChatKind = conversation.ChatDirect
	candidate.SenderName = "Ayu"
	candidate.Allowlisted = true
	candidate.OccurredAt, candidate.ReceivedAt = now, now
	claimed, err := store.Inbound().ClaimAndResolveSender(ctx, candidate)
	if err != nil {
		t.Fatalf("claim incoming message: %v", err)
	}
	key := agent.Key{TenantID: tenantID, AccountID: accountID, ChatID: claimed.Message.ChatID}
	config, err := store.Configs().LoadOrCreate(ctx, key, testDefaults(t))
	if err != nil {
		t.Fatalf("create chat config: %v", err)
	}
	assistantID, _ := identity.NewMessageID()
	assistantEntry := agent.HistoryEntry{
		MessageID: assistantID, InvocationID: claimed.Message.InvocationID,
		Causation: agent.CausationRef{Kind: agent.CausationMessage, ID: claimed.Message.CausationID},
		Role:      agent.HistoryAssistant, Content: []agent.ContentPart{agent.TextPart{Text: "Halo, Ayu."}},
		Delivery: agent.DeliverySucceeded, CreatedAt: now.Add(time.Second),
	}
	if err := store.History().Append(ctx, key, assistantEntry); err != nil {
		t.Fatalf("append assistant response: %v", err)
	}

	reader, err := NewConversationReader(dataRoot)
	if err != nil {
		t.Fatalf("create reader: %v", err)
	}
	scope := control.SessionScope{TenantID: tenantID, AccountID: accountID}
	conversations, err := reader.ListBotConversations(ctx, scope, transcriptPageSize)
	if err != nil || len(conversations) != 1 {
		t.Fatalf("list conversations = %#v, err=%v", conversations, err)
	}
	chat := conversations[0]
	if chat.ID != claimed.Message.ChatID || chat.Kind != "direct" || chat.Name != "Ayu" ||
		chat.LastMessage != "Halo, Ayu." || !chat.LastFromBot || chat.MessageCount != 2 {
		t.Fatalf("unexpected bot conversation: %#v", chat)
	}

	messages, err := reader.ListBotMessages(ctx, scope, chat.ID, transcriptPageSize)
	if err != nil || len(messages) != 2 {
		t.Fatalf("list messages = %#v, err=%v", messages, err)
	}
	if messages[0].Role != "user" || messages[0].Sender != "Ayu" || messages[0].Content != candidate.Text ||
		messages[1].Role != "assistant" || messages[1].Sender != "Bot" || messages[1].Delivery != "sent" {
		t.Fatalf("unexpected transcript order/content: %#v", messages)
	}

	readOnlyDB, exists, err := reader.open(ctx, scope)
	if err != nil || !exists {
		t.Fatalf("open read-only transcript store: exists=%v err=%v", exists, err)
	}
	defer readOnlyDB.Close()
	if _, err := readOnlyDB.ExecContext(ctx, `DELETE FROM history_entries`); err == nil {
		t.Fatal("transcript reader unexpectedly allowed a database write")
	}

	if err := store.History().ResetIfConfigVersion(ctx, key, config.Version, now.Add(2*time.Second)); err != nil {
		t.Fatalf("reset transcript: %v", err)
	}
	conversations, err = reader.ListBotConversations(ctx, scope, transcriptPageSize)
	if err != nil || len(conversations) != 0 {
		t.Fatalf("history reset should hide the chat: %#v, err=%v", conversations, err)
	}
}

func TestConversationReaderLoadsPassiveGroupHistoryWithoutAgentConfig(t *testing.T) {
	ctx := context.Background()
	dataRoot := t.TempDir()
	tenantID, _ := identity.NewTenantID()
	accountID, _ := identity.NewAccountID()
	path := filepath.Join(dataRoot, "tenants", tenantID.String(), "app.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open app database: %v", err)
	}
	defer store.Close()

	candidate := testCandidate(t, "passive-group-history", "120363000000000002@g.us")
	candidate.TenantID, candidate.AccountID = tenantID, accountID
	candidate.ChatKind = conversation.ChatGroup
	candidate.SenderName = "Rina"
	candidate.Allowlisted = true
	claimed, err := store.Inbound().ClaimAndResolveSender(ctx, candidate)
	if err != nil {
		t.Fatalf("claim passive group message: %v", err)
	}
	key := agent.Key{TenantID: tenantID, AccountID: accountID, ChatID: claimed.Message.ChatID}
	if _, err := store.Configs().Load(ctx, key); !agent.IsCode(err, agent.ErrorNotFound) {
		t.Fatalf("passive group unexpectedly has an Agent config: %v", err)
	}

	reader, err := NewConversationReader(dataRoot)
	if err != nil {
		t.Fatalf("create reader: %v", err)
	}
	messages, err := reader.ListBotMessages(ctx, control.SessionScope{TenantID: tenantID, AccountID: accountID}, key.ChatID, transcriptPageSize)
	if err != nil || len(messages) != 1 {
		t.Fatalf("read passive group transcript = %#v, err=%v", messages, err)
	}
	if messages[0].Role != "user" || messages[0].Sender != "Rina" || messages[0].Content != candidate.Text {
		t.Fatalf("unexpected passive group message: %#v", messages[0])
	}
	if _, err := store.Configs().Load(ctx, key); !agent.IsCode(err, agent.ErrorNotFound) {
		t.Fatalf("transcript read created an Agent config: %v", err)
	}
}

func TestConversationReaderUsesPersistedGroupName(t *testing.T) {
	ctx := context.Background()
	dataRoot := t.TempDir()
	tenantID, _ := identity.NewTenantID()
	accountID, _ := identity.NewAccountID()
	path := filepath.Join(dataRoot, "tenants", tenantID.String(), "app.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open app database: %v", err)
	}
	candidate := testCandidate(t, "group-name-message", "120363000000000001@g.us")
	candidate.TenantID, candidate.AccountID = tenantID, accountID
	candidate.ChatKind = conversation.ChatGroup
	candidate.SenderName = "Rina"
	candidate.Allowlisted = true
	if _, err := store.Inbound().ClaimAndResolveSender(ctx, candidate); err != nil {
		t.Fatalf("claim group message: %v", err)
	}
	reader, err := NewConversationReader(dataRoot)
	if err != nil {
		t.Fatalf("create reader: %v", err)
	}
	scope := control.SessionScope{TenantID: tenantID, AccountID: accountID}
	assertName := func(want string) {
		t.Helper()
		chats, readErr := reader.ListBotConversations(ctx, scope, transcriptPageSize)
		if readErr != nil || len(chats) != 1 || chats[0].Name != want {
			t.Fatalf("group name = %#v, err=%v; want %q", chats, readErr, want)
		}
	}
	assertName("Group")
	if err := store.Inbound().SaveGroupName(ctx, tenantID, accountID, candidate.ProviderChatAddress, "  Keluarga  "); err != nil {
		t.Fatalf("save group name: %v", err)
	}
	assertName("Keluarga")
	otherAccountID, _ := identity.NewAccountID()
	if err := store.Inbound().SaveGroupName(ctx, tenantID, otherAccountID, candidate.ProviderChatAddress, "Nama akun lain"); err != nil {
		t.Fatalf("save name for another account: %v", err)
	}
	assertName("Keluarga")
	if err := store.Inbound().SaveGroupName(ctx, tenantID, accountID, candidate.ProviderChatAddress, "Keluarga Besar"); err != nil {
		t.Fatalf("save renamed group: %v", err)
	}
	assertName("Keluarga Besar")
	if err := store.Close(); err != nil {
		t.Fatalf("close app database: %v", err)
	}
	assertName("Keluarga Besar")

	// The read-only Chat page can still show existing history before an older
	// app database receives the new migration at the next Agent start.
	legacyDB, err := sql.Open("sqlite", databaseDSN(path, defaultBusyTimeoutMS))
	if err != nil {
		t.Fatalf("open old-schema fixture: %v", err)
	}
	if _, err := legacyDB.ExecContext(ctx, `ALTER TABLE chats DROP COLUMN group_name`); err != nil {
		_ = legacyDB.Close()
		t.Fatalf("remove new column from fixture: %v", err)
	}
	if err := legacyDB.Close(); err != nil {
		t.Fatalf("close old-schema fixture: %v", err)
	}
	assertName("Group")
}
