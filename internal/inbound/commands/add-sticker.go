package commands

import (
	"context"
	"strings"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/sticker"
)

func init() {
	register(command.Command{
		Name:        "add-sticker",
		Aliases:     []string{"addsticker"},
		Permission:  "(private or admin or owner) and !fromMe",
		Description: "Saves a sticker under a name so the AI can send it in this chat. Reply to a sticker (or an image or video) with /add-sticker <name>.",
		DeniedReply: "In a group, only an admin or the owner can add stickers.",
		Run:         runAddSticker,
	})
}

func runAddSticker(ctx context.Context, c *command.Context) error {
	catalog, err := c.Stickers()
	if err != nil {
		return err
	}
	name := strings.ToLower(strings.TrimSpace(c.Args))
	if name == "" {
		return replyAddStickerCatalog(ctx, c, catalog, "Usage: reply to a sticker, image or video with /add-sticker <name>. Example: /add-sticker funny_cat")
	}
	if !sticker.ValidName(name) {
		return c.Reply(ctx, "A sticker name uses only a-z, 0-9, _ and -, up to 64 characters. Example: /add-sticker funny_cat")
	}
	media, err := c.Media(ctx)
	if agent.IsCode(err, agent.ErrorNotFound) {
		return c.Reply(ctx, "Reply to a sticker, image or video with /add-sticker "+name+".")
	}
	if err != nil {
		return err
	}
	value := sticker.Sticker{Name: name, WebP: media.Data, Animated: media.Animated, Lottie: media.Lottie}
	if media.Kind != command.MediaSticker {
		made, err := sticker.Make(ctx, media.Data, media.Kind == command.MediaVideo, sticker.Text{})
		if err != nil {
			return c.Reply(ctx, sticker.Problem(err))
		}
		value.WebP, value.Animated = made.WebP, made.Animated
	}
	replaced, err := catalog.SaveSticker(ctx, c.Key(), value)
	if err != nil {
		return err
	}
	if replaced {
		return c.Reply(ctx, "Sticker "+name+" was replaced.")
	}
	return c.Reply(ctx, "Sticker "+name+" was added. The AI can now send it in this chat.")
}

// replyAddStickerCatalog sends text followed by the chat's sticker names.
func replyAddStickerCatalog(ctx context.Context, c *command.Context, catalog sticker.Catalog, text string) error {
	names, err := catalog.StickerNames(ctx, c.Key())
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return c.Reply(ctx, text+"\n\nThis chat has no stickers yet.")
	}
	return c.Reply(ctx, text+"\n\nStickers in this chat: "+strings.Join(names, ", "))
}
