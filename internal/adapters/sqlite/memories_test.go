package sqlite

import (
	"context"
	"strings"
	"testing"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
)

func TestMemoriesArePerChatOrderedAndDeduplicated(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	first, err := store.Inbound().ClaimAndResolveSender(ctx, testCandidate(t, "memory-chat-1", "15550000201@s.whatsapp.net"))
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.Inbound().ClaimAndResolveSender(ctx, testCandidate(t, "memory-chat-2", "15550000202@s.whatsapp.net"))
	if err != nil {
		t.Fatal(err)
	}
	key := agent.Key{TenantID: first.Message.TenantID, AccountID: first.Message.AccountID, ChatID: first.Message.ChatID}
	otherKey := agent.Key{TenantID: other.Message.TenantID, AccountID: other.Message.AccountID, ChatID: other.Message.ChatID}
	memories := store.Memories()

	cats, added, err := memories.AddMemory(ctx, key, "  likes cats ")
	if err != nil || !added || len(cats.Code) != memoryCodeLength || cats.Text != "likes cats" {
		t.Fatalf("add = %#v, %v, %v", cats, added, err)
	}
	again, added, err := memories.AddMemory(ctx, key, "likes cats")
	if err != nil || added || again != cats {
		t.Fatalf("duplicate add = %#v, %v, %v", again, added, err)
	}
	dogs, _, err := memories.AddMemory(ctx, key, "likes dogs")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := memories.AddMemory(ctx, otherKey, "likes cats"); err != nil {
		t.Fatalf("same text in another chat: %v", err)
	}
	if _, _, err := memories.AddMemory(ctx, key, strings.Repeat("x", agent.MaxMemoryRunes+1)); !agent.IsCode(err, agent.ErrorInvalidArgument) {
		t.Fatalf("too long err = %v", err)
	}

	listed, err := memories.Memories(ctx, key)
	if err != nil || len(listed) != 2 || listed[0] != cats || listed[1] != dogs {
		t.Fatalf("list = %v, err=%v", listed, err)
	}
	deleted, err := memories.DeleteMemories(ctx, key, []string{strings.ToUpper(cats.Code), "zzzz", cats.Code})
	if err != nil || len(deleted) != 1 || deleted[0] != cats {
		t.Fatalf("delete = %v, err=%v", deleted, err)
	}
	if listed, _ := memories.Memories(ctx, otherKey); len(listed) != 1 {
		t.Fatalf("other chat lost its memory: %v", listed)
	}

	for index := 1; index < agent.MaxMemories; index++ {
		if _, _, err := memories.AddMemory(ctx, otherKey, "note "+strings.Repeat("n", index)); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := memories.AddMemory(ctx, otherKey, "one too many"); !agent.IsCode(err, agent.ErrorResourceExhausted) {
		t.Fatalf("full memory err = %v", err)
	}
}

func TestMessageMediaIsReadByMessageID(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	candidate := testCandidate(t, "sticker-message", "15550000203@s.whatsapp.net")
	candidate.Text = "a sticker"
	candidate.ProviderMediaJSON = []byte(`{"stickerMessage":{"mimetype":"image/webp"}}`)
	claimed, err := store.Inbound().ClaimAndResolveSender(ctx, candidate)
	if err != nil {
		t.Fatal(err)
	}
	key := agent.Key{TenantID: claimed.Message.TenantID, AccountID: claimed.Message.AccountID, ChatID: claimed.Message.ChatID}
	payload, err := store.Inbound().ReadMessageMedia(ctx, key, claimed.Message.ID)
	if err != nil || string(payload) != string(candidate.ProviderMediaJSON) {
		t.Fatalf("payload = %s, err=%v", payload, err)
	}
	plain, err := store.Inbound().ClaimAndResolveSender(ctx, testCandidate(t, "plain-message", "15550000204@s.whatsapp.net"))
	if err != nil {
		t.Fatal(err)
	}
	plainKey := agent.Key{TenantID: plain.Message.TenantID, AccountID: plain.Message.AccountID, ChatID: plain.Message.ChatID}
	if _, err := store.Inbound().ReadMessageMedia(ctx, plainKey, plain.Message.ID); !agent.IsCode(err, agent.ErrorNotFound) {
		t.Fatalf("message without media err = %v", err)
	}
	if _, err := store.Inbound().ReadMessageMedia(ctx, plainKey, claimed.Message.ID); !agent.IsCode(err, agent.ErrorNotFound) {
		t.Fatalf("message from another chat err = %v", err)
	}
}
