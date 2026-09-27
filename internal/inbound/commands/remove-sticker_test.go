package commands

import (
	"strings"
	"testing"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/sticker"
)

func TestRemoveStickerDeletesByName(t *testing.T) {
	stickers := &recordingStickers{catalog: map[string]sticker.Sticker{"wave": {}, "cat": {}}}
	replies, err := runStickerCommand(t, "/remove-sticker WAVE", command.PermissionFacts{IsPrivate: true}, nil, stickers)
	if err != nil || len(replies.sent) != 1 || !strings.Contains(replies.sent[0], "wave was removed") {
		t.Fatalf("err=%v replies=%q", err, replies.sent)
	}
	if _, found := stickers.catalog["wave"]; found || len(stickers.catalog) != 1 {
		t.Fatalf("catalog = %v", stickers.catalog)
	}
	replies, err = runStickerCommand(t, "/remove-sticker wave", command.PermissionFacts{IsPrivate: true}, nil, stickers)
	if err != nil || len(replies.sent) != 1 || !strings.Contains(replies.sent[0], "no sticker named wave") || !strings.Contains(replies.sent[0], "cat") {
		t.Fatalf("missing name: err=%v replies=%q", err, replies.sent)
	}
}
