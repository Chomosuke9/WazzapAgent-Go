package commands

import (
	"context"
	"strings"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/sticker"
)

func init() {
	register(command.Command{
		Name:        "remove-sticker",
		Aliases:     []string{"removesticker"},
		Permission:  "(private or admin or owner) and !fromMe",
		Description: "Removes a saved sticker from this chat by name: /remove-sticker <name>.",
		DeniedReply: "In a group, only an admin or the owner can remove stickers.",
		Run:         runRemoveSticker,
	})
}

func runRemoveSticker(ctx context.Context, c *command.Context) error {
	catalog, err := c.Stickers()
	if err != nil {
		return err
	}
	name := strings.ToLower(strings.TrimSpace(c.Args))
	if name == "" {
		return replyRemoveStickerCatalog(ctx, c, catalog, "Usage: /remove-sticker <name>")
	}
	deleted, err := catalog.DeleteSticker(ctx, c.Key(), name)
	if err != nil {
		return err
	}
	if !deleted {
		return replyRemoveStickerCatalog(ctx, c, catalog, "This chat has no sticker named "+name+".")
	}
	return c.Reply(ctx, "Sticker "+name+" was removed.")
}

// replyRemoveStickerCatalog sends text followed by the chat's sticker names.
func replyRemoveStickerCatalog(ctx context.Context, c *command.Context, catalog sticker.Catalog, text string) error {
	names, err := catalog.StickerNames(ctx, c.Key())
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return c.Reply(ctx, text+"\n\nThis chat has no stickers.")
	}
	return c.Reply(ctx, text+"\n\nStickers in this chat: "+strings.Join(names, ", "))
}
