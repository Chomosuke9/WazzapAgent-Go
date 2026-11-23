package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/sticker"
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

func TestTheBotMakesAStickerOfItsTargetMessage(t *testing.T) {
	registry := builtinRegistry(t)
	request, _, _ := registry.Parse("/sticker #gas")
	key := testChatKey(t)
	commandID, _ := identity.NewMessageID()
	target, _ := identity.NewMessageID()
	stickers := &recordingStickers{}
	replies := &recordingText{}
	// A command the bot runs has no inbox record, so no Store: the media
	// comes from the message it targets.
	err := registry.Dispatch(context.Background(), request, command.Invocation{
		Message: conversation.IncomingMessage{TenantID: key.TenantID, AccountID: key.AccountID, ChatID: key.ChatID,
			ID: commandID, Text: "/sticker #gas", FromMe: true,
			Quote: &conversation.QuotedMessage{ID: target, Role: conversation.QuoteUser, Text: "command target"}},
		Facts: command.PermissionFacts{IsGroup: true, FromMe: true},
		Platform: command.Platform{Text: replies, Stickers: stickers, Catalog: stickers,
			Media:        fakeMedia{media: command.Media{Kind: command.MediaImage, Data: testPNGBytes(t)}},
			MessageMedia: targetMedia{payloads: map[identity.MessageID][]byte{target: []byte(`{}`)}}},
	})
	if err != nil || len(stickers.sent) != 1 || len(replies.sent) != 0 || len(stickers.sent[0].WebP) == 0 {
		t.Fatalf("err=%v stickers=%d replies=%q", err, len(stickers.sent), replies.sent)
	}
	names, err := modelCommands(command.PermissionFacts{IsGroup: true, FromMe: true})
	if err != nil || !names["sticker"] {
		t.Fatalf("the model is not offered /sticker: %v, %v", names, err)
	}
}

// modelCommands is the set of commands the model may issue under facts.
func modelCommands(facts command.PermissionFacts) (map[string]bool, error) {
	names := map[string]bool{}
	for _, cmd := range registered {
		allowed, err := command.EvaluatePermission(cmd.Permission, facts)
		if err != nil {
			return nil, err
		}
		names[cmd.Name] = allowed
	}
	return names, nil
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
