package commands

import (
	"context"
	"strings"

	"github.com/Chomosuke9/DiscordAgent-Go/internal/agent"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/command"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/sticker"
)

func init() {
	register(command.Command{
		Name:        "sticker",
		Aliases:     []string{"stiker"},
		Permission:  "public and !fromMe",
		Description: "Turns an image, GIF, video or sticker into a sticker. Send it with the caption /sticker, or reply to it with /sticker. Add meme text with /sticker top text#bottom text.",
		Run:         runSticker,
	})
}

const stickerUsage = "Send an image, GIF or video with the caption /sticker, or reply to one (or to a sticker) with /sticker.\n" +
	"Add meme text with /sticker top text#bottom text, for example: /sticker so me#when monday arrives"

func runSticker(ctx context.Context, c *command.Context) error {
	media, err := c.Media(ctx)
	if agent.IsCode(err, agent.ErrorNotFound) {
		return c.Reply(ctx, stickerUsage)
	}
	if err != nil {
		return err
	}
	if len(media.Lottie) > 0 {
		return c.Reply(ctx, "That is an animated WhatsApp sticker, which can't be converted. Use /add-sticker to save it instead.")
	}
	made, err := sticker.Make(ctx, media.Data, media.Kind == command.MediaVideo, parseStickerText(c.Args))
	if err != nil {
		return c.Reply(ctx, sticker.Problem(err))
	}
	return c.SendSticker(ctx, sticker.Sticker{WebP: made.WebP, Animated: made.Animated})
}

// parseStickerText splits "/sticker top#bottom" meme text. Without "#" the
// whole text goes on top.
func parseStickerText(args string) sticker.Text {
	top, bottom, _ := strings.Cut(args, "#")
	return sticker.Text{Top: strings.TrimSpace(top), Bottom: strings.TrimSpace(bottom)}
}
