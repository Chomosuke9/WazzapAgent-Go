package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
)

func init() {
	register(command.Command{
		Name:        "prompt",
		Permission:  "(owner or (admin and group)) and !fromMe",
		Description: "Views, updates, or deletes the custom chat prompt.",
		DeniedReply: "The /prompt command can only be used by group admins or the owner.",
		Run:         runPrompt,
	})
}

func runPrompt(ctx context.Context, c *command.Context) error {
	switch action, text := parsePromptArgs(c.Args, c.HasArgs); action {
	case "view":
		if c.Config.PromptOverride == nil {
			return c.Reply(ctx, "No prompt override is configured.")
		}
		return c.Reply(ctx, "Current prompt override:\n"+c.Config.PromptOverride.Text)
	case "set":
		if _, err := c.UpdateConfig(ctx, func(values *agent.ConfigValues) {
			values.PromptOverride = &agent.PromptOverride{Mode: agent.PromptAppend, Text: text}
		}); err != nil {
			return err
		}
		return c.Reply(ctx, "Prompt override updated.")
	case "clear":
		if _, err := c.UpdateConfig(ctx, func(values *agent.ConfigValues) {
			values.PromptOverride = nil
		}); err != nil {
			return err
		}
		return c.Reply(ctx, "Prompt override deleted.")
	default:
		return c.Reply(ctx, fmt.Sprintf("Usage: /prompt view, /prompt set <text>, or /prompt clear. The prompt can be up to %d bytes long.", agent.MaxPromptBytes))
	}
}

// parsePromptArgs returns "view", "set" with its text, "clear", or "" for
// invalid arguments.
func parsePromptArgs(args string, hasArgs bool) (action, text string) {
	switch {
	case !hasArgs || args == "view":
		return "view", ""
	case args == "clear":
		return "clear", ""
	case strings.HasPrefix(args, "set "):
		value := strings.TrimPrefix(args, "set ")
		if strings.TrimSpace(value) != "" && len(value) <= agent.MaxPromptBytes {
			return "set", value
		}
	}
	return "", ""
}
