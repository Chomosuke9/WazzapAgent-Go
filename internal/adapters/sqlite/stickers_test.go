package sqlite

import (
	"context"
	"reflect"
	"testing"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/sticker"
)

func TestStickerCatalogIsPerChatAndReplacesByName(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	first, err := store.Inbound().ClaimAndResolveSender(ctx, testCandidate(t, "sticker-chat-1", "15550000101@s.whatsapp.net"))
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.Inbound().ClaimAndResolveSender(ctx, testCandidate(t, "sticker-chat-2", "15550000102@s.whatsapp.net"))
	if err != nil {
		t.Fatal(err)
	}
	key := agent.Key{TenantID: first.Message.TenantID, AccountID: first.Message.AccountID, ChatID: first.Message.ChatID}
	otherKey := agent.Key{TenantID: other.Message.TenantID, AccountID: other.Message.AccountID, ChatID: other.Message.ChatID}
	catalog := store.Stickers()

	if replaced, err := catalog.SaveSticker(ctx, key, sticker.Sticker{Name: "wave", WebP: []byte("one")}); err != nil || replaced {
		t.Fatalf("first save replaced=%v err=%v", replaced, err)
	}
	if replaced, err := catalog.SaveSticker(ctx, key, sticker.Sticker{Name: "wave", Lottie: []byte(`{"isLottie":true}`), Animated: true}); err != nil || !replaced {
		t.Fatalf("second save replaced=%v err=%v", replaced, err)
	}
	if _, err := catalog.SaveSticker(ctx, key, sticker.Sticker{Name: "cat", WebP: []byte("cat")}); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.SaveSticker(ctx, key, sticker.Sticker{Name: "Bad Name", WebP: []byte("x")}); !agent.IsCode(err, agent.ErrorInvalidArgument) {
		t.Fatalf("invalid name err = %v", err)
	}

	names, err := catalog.StickerNames(ctx, key)
	if err != nil || !reflect.DeepEqual(names, []string{"cat", "wave"}) {
		t.Fatalf("names = %v, err=%v", names, err)
	}
	if names, err := catalog.StickerNames(ctx, otherKey); err != nil || len(names) != 0 {
		t.Fatalf("other chat names = %v, err=%v", names, err)
	}
	loaded, err := catalog.LoadSticker(ctx, key, "wave")
	if err != nil || len(loaded.WebP) != 0 || string(loaded.Lottie) != `{"isLottie":true}` || !loaded.Animated {
		t.Fatalf("loaded = %#v, err=%v", loaded, err)
	}
	if _, err := catalog.LoadSticker(ctx, otherKey, "wave"); !agent.IsCode(err, agent.ErrorNotFound) {
		t.Fatalf("other chat load err = %v", err)
	}
	if deleted, err := catalog.DeleteSticker(ctx, key, "wave"); err != nil || !deleted {
		t.Fatalf("delete = %v, err=%v", deleted, err)
	}
	if deleted, err := catalog.DeleteSticker(ctx, key, "wave"); err != nil || deleted {
		t.Fatalf("second delete = %v, err=%v", deleted, err)
	}
}

func TestCommandMediaIsStoredWithTheCommandMessage(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	candidate := testCandidate(t, "sticker-command", "15550000103@s.whatsapp.net")
	candidate.Text = "/sticker"
	candidate.ProviderMediaJSON = []byte(`{"imageMessage":{"mimetype":"image/jpeg"}}`)
	claimed, err := store.Inbound().ClaimAndResolveSender(ctx, candidate)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := store.Inbound().ReadCommandMedia(ctx, claimed.Message)
	if err != nil || string(payload) != string(candidate.ProviderMediaJSON) {
		t.Fatalf("payload = %s, err=%v", payload, err)
	}

	plain, err := store.Inbound().ClaimAndResolveSender(ctx, testCandidate(t, "plain-command", "15550000104@s.whatsapp.net"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Inbound().ReadCommandMedia(ctx, plain.Message); !agent.IsCode(err, agent.ErrorNotFound) {
		t.Fatalf("message without media err = %v", err)
	}
}
