package commands

import (
	"context"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
)

func init() {
	register(command.Command{
		Name:        "reset",
		Permission:  "owner and !fromMe",
		Description: "Clears the conversation history for this chat.",
		DeniedReply: "The /reset command can only be used by the configured owner.",
		Run:         runReset,
	})
}

func runReset(ctx context.Context, c *command.Context) error {
	if c.HasArgs {
		return c.Reply(ctx, "The /reset command does not accept arguments.")
	}
	if err := c.ResetHistory(ctx); err != nil {
		return err
	}
	return c.Reply(ctx, "Conversation history was reset.")
}
