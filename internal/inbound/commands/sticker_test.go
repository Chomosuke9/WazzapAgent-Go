package commands

import (
	"strings"
	"testing"

	"github.com/Chomosuke9/DiscordAgent-Go/internal/command"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/sticker"
)

func TestStickerTurnsAnImageIntoAQuotedSticker(t *testing.T) {
	stickers := &recordingStickers{}
	replies, err := runStickerCommand(t, "/sticker top#bottom", command.PermissionFacts{IsPrivate: true},
		&command.Media{Kind: command.MediaImage, Data: testPNGBytes(t)}, stickers)
	if err != nil || len(replies.sent) != 0 || len(stickers.sent) != 1 {
		t.Fatalf("err=%v replies=%q stickers=%d", err, replies.sent, len(stickers.sent))
	}
	if len(stickers.sent[0].WebP) == 0 || stickers.sent[0].Animated || stickers.quoted[0].IsZero() {
		t.Fatalf("sent sticker = %d bytes, animated=%v, quoted=%v", len(stickers.sent[0].WebP), stickers.sent[0].Animated, stickers.quoted[0])
	}
}

func TestStickerWithoutMediaRepliesWithUsage(t *testing.T) {
	stickers := &recordingStickers{}
	replies, err := runStickerCommand(t, "/sticker", command.PermissionFacts{IsPrivate: true}, nil, stickers)
	if err != nil || len(replies.sent) != 1 || !strings.Contains(replies.sent[0], "caption /sticker") || len(stickers.sent) != 0 {
		t.Fatalf("err=%v replies=%q", err, replies.sent)
	}
}

func TestStickerExplainsLottieAndUnreadableFiles(t *testing.T) {
	for _, media := range []command.Media{
		{Kind: command.MediaSticker, Lottie: []byte(`{}`), Animated: true},
		{Kind: command.MediaImage, Data: []byte("not an image")},
	} {
		stickers := &recordingStickers{}
		replies, err := runStickerCommand(t, "/sticker", command.PermissionFacts{IsPrivate: true}, &media, stickers)
		if err != nil || len(replies.sent) != 1 || len(stickers.sent) != 0 {
			t.Fatalf("media %v: err=%v replies=%q", media.Kind, err, replies.sent)
		}
	}
}

func TestStickerIsNotOfferedToTheModel(t *testing.T) {
	_, err := runStickerCommand(t, "/sticker", command.PermissionFacts{IsPrivate: true, FromMe: true}, nil, &recordingStickers{})
	if err == nil {
		t.Fatal("/sticker ran for fromMe")
	}
}

func TestParseStickerText(t *testing.T) {
	for args, want := range map[string]sticker.Text{
		"":                 {},
		"hello":            {Top: "hello"},
		" so me # monday ": {Top: "so me", Bottom: "monday"},
		"#only bottom":     {Bottom: "only bottom"},
		"a#b#c":            {Top: "a", Bottom: "b#c"},
	} {
		if got := parseStickerText(args); got != want {
			t.Errorf("parseStickerText(%q) = %#v, want %#v", args, got, want)
		}
	}
}
