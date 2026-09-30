package commands

import (
	"errors"
	"strings"
	"testing"

	"github.com/Chomosuke9/DiscordAgent-Go/internal/command"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/sticker"
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
