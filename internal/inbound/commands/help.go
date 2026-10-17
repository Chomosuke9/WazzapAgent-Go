package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
)

func init() {
	register(command.Command{
		Name:        "help",
		Aliases:     []string{"menu"},
		Permission:  "public and !fromMe",
		Description: "Shows the list of available commands.",
		Run:         runHelp,
	})
}

func runHelp(ctx context.Context, c *command.Context) error {
	if c.HasArgs {
		return c.Reply(ctx, "The /help command does not accept arguments.")
	}
	lines := []string{"Available commands:"}
	for _, cmd := range c.Commands() {
		aliases := ""
		if len(cmd.Aliases) > 0 {
			aliases = " (alias: /" + strings.Join(cmd.Aliases, ", /") + ")"
		}
		lines = append(lines, fmt.Sprintf("/%s%s - %s", cmd.Name, aliases, cmd.Description))
	}
	return c.Reply(ctx, strings.Join(lines, "\n"))
}
