package commands

import (
	"context"
	"fmt"

	"github.com/Chomosuke9/DiscordAgent-Go/internal/agent"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/command"
)

func init() {
	register(command.Command{
		Name:        "info",
		Permission:  "public and !fromMe",
		Description: "Shows the Agent, model, configuration, and history status.",
		Run:         runInfo,
	})
}

func runInfo(ctx context.Context, c *command.Context) error {
	if c.HasArgs {
		return c.Reply(ctx, "The /info command does not accept arguments.")
	}
	page, err := c.Agent.History().List(ctx, c.Config.Version, agent.HistoryQuery{Limit: 1})
	if err != nil {
		return err
	}
	historyState := "empty"
	if len(page.Entries) > 0 {
		historyState = "active"
	}
	return c.Reply(ctx, fmt.Sprintf("Agent is active. Model: %s. Config version: %d. History: %s.", c.Config.Model.Model, c.Config.Version, historyState))
}
