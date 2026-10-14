package commands

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/sticker"
)

func TestAddStickerSavesARepliedStickerAsIs(t *testing.T) {
	stickers := &recordingStickers{}
	media := &command.Media{Kind: command.MediaSticker, Data: []byte("webp bytes"), Animated: true}
	replies, err := runStickerCommand(t, "/add-sticker Funny_Cat", command.PermissionFacts{IsPrivate: true}, media, stickers)
	if err != nil || len(replies.sent) != 1 || !strings.Contains(replies.sent[0], "funny_cat was added") {
		t.Fatalf("err=%v replies=%q", err, replies.sent)
	}
	saved := stickers.catalog["funny_cat"]
	if string(saved.WebP) != "webp bytes" || !saved.Animated {
		t.Fatalf("saved = %#v", saved)
	}
}

func TestAddStickerConvertsAnImage(t *testing.T) {
	stickers := &recordingStickers{}
	media := &command.Media{Kind: command.MediaImage, Data: testPNGBytes(t)}
	if _, err := runStickerCommand(t, "/add-sticker wave", command.PermissionFacts{IsPrivate: true}, media, stickers); err != nil {
		t.Fatal(err)
	}
	if saved := stickers.catalog["wave"]; len(saved.WebP) < 12 || string(saved.WebP[8:12]) != "WEBP" {
		t.Fatalf("saved image was not converted to WebP: %q", saved.WebP)
	}
}

func TestAddStickerRejectsBadNamesAndListsTheCatalog(t *testing.T) {
	stickers := &recordingStickers{}
	media := &command.Media{Kind: command.MediaSticker, Data: []byte("x")}
	replies, err := runStickerCommand(t, "/add-sticker not ok!", command.PermissionFacts{IsPrivate: true}, media, stickers)
	if err != nil || len(replies.sent) != 1 || len(stickers.catalog) != 0 {
		t.Fatalf("bad name: err=%v replies=%q catalog=%v", err, replies.sent, stickers.catalog)
	}
	replies, err = runStickerCommand(t, "/add-sticker", command.PermissionFacts{IsPrivate: true}, nil, stickers)
	if err != nil || len(replies.sent) != 1 || !strings.Contains(replies.sent[0], "no stickers yet") {
		t.Fatalf("usage: err=%v replies=%q", err, replies.sent)
	}
	stickers.catalog = map[string]sticker.Sticker{"cat": {}, "wave": {}}
	replies, err = runStickerCommand(t, "/add-sticker", command.PermissionFacts{IsPrivate: true}, nil, stickers)
	if err != nil || len(replies.sent) != 1 || !strings.Contains(replies.sent[0], "Stickers in this chat: cat, wave") {
		t.Fatalf("usage with catalog: err=%v replies=%q", err, replies.sent)
	}
}

func TestAddStickerNeedsAdminInGroups(t *testing.T) {
	_, err := runStickerCommand(t, "/add-sticker wave", command.PermissionFacts{IsGroup: true}, nil, &recordingStickers{})
	if !errors.Is(err, command.ErrDenied) {
		t.Fatalf("member in group err = %v, want denied", err)
	}
	if _, err := runStickerCommand(t, "/add-sticker", command.PermissionFacts{IsGroup: true, IsAdmin: true}, nil, &recordingStickers{}); err != nil {
		t.Fatalf("admin in group err = %v", err)
	}
}

// targetMedia reads the media of the message a bot-run command targets.
type targetMedia struct {
	payloads map[identity.MessageID][]byte
}

func (reader targetMedia) ReadMessageMedia(_ context.Context, _ agent.Key, message identity.MessageID) ([]byte, error) {
	if payload, found := reader.payloads[message]; found {
		return payload, nil
	}
	return nil, agent.NewError(agent.ErrorNotFound, "read message media", errors.New("none"))
}

func TestTheBotAddsTheStickerOfItsTargetMessage(t *testing.T) {
	registry := builtinRegistry(t)
	request, _, _ := registry.Parse("/add-sticker party")
	key := testChatKey(t)
	commandID, _ := identity.NewMessageID()
	target, _ := identity.NewMessageID()
	stickers := &recordingStickers{}
	replies := &recordingText{}
	run := func(quote *conversation.QuotedMessage) error {
		// A command the bot runs has no inbox record, so no Store.
		return registry.Dispatch(context.Background(), request, command.Invocation{
			Message: conversation.IncomingMessage{TenantID: key.TenantID, AccountID: key.AccountID, ChatID: key.ChatID,
				ID: commandID, Text: "/add-sticker party", FromMe: true, Quote: quote},
			Facts: command.PermissionFacts{IsGroup: true, FromMe: true},
			Platform: command.Platform{Text: replies, Stickers: stickers, Catalog: stickers,
				Media:        fakeMedia{media: command.Media{Kind: command.MediaSticker, Data: []byte("webp"), Animated: true}},
				MessageMedia: targetMedia{payloads: map[identity.MessageID][]byte{target: []byte(`{}`)}}},
		})
	}
	if err := run(&conversation.QuotedMessage{ID: target, Role: conversation.QuoteUser, Text: "command target"}); err != nil {
		t.Fatal(err)
	}
	if saved := stickers.catalog["party"]; string(saved.WebP) != "webp" || !saved.Animated || len(replies.sent) != 1 || !strings.Contains(replies.sent[0], "party was added") {
		t.Fatalf("catalog=%v replies=%q", stickers.catalog, replies.sent)
	}
	other, _ := identity.NewMessageID()
	if err := run(&conversation.QuotedMessage{ID: other, Role: conversation.QuoteUser, Text: "command target"}); err != nil {
		t.Fatal(err)
	}
	if err := run(nil); err != nil {
		t.Fatal(err)
	}
	if len(replies.sent) != 3 || !strings.Contains(replies.sent[1], "Reply to a sticker") || !strings.Contains(replies.sent[2], "Reply to a sticker") {
		t.Fatalf("targets without media: replies=%q", replies.sent)
	}
}
