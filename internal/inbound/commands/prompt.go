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
		Name:       "prompt",
		Aliases:    []string{"instructions"},
		Permission: "owner or (admin and group) or fromMe",
		Description: "Shows and changes this chat's custom instructions: /prompt shows them; /prompt set <text> adds them to the main prompt; " +
			"/prompt replace <text> uses them instead of the main prompt; /prompt clear deletes them.",
		DeniedReply: "The /prompt command can only be used by group admins or the owner.",
		Run:         runPrompt,
	})
}

func runPrompt(ctx context.Context, c *command.Context) error {
	switch action, text := parsePromptArgs(c.Args, c.HasArgs); action {
	case "view":
		return replyPrompt(ctx, c, "", c.Config.PromptOverride)
	case "set", "replace":
		mode, done := agent.PromptAppend, "✅ Custom instructions saved. I follow them on top of the main prompt."
		if action == "replace" {
			mode, done = agent.PromptReplace, "✅ Custom instructions saved. I follow them instead of the main prompt."
		}
		override := &agent.PromptOverride{Mode: mode, Text: text}
		if _, err := c.UpdateConfig(ctx, func(values *agent.ConfigValues) { values.PromptOverride = override }); err != nil {
			return err
		}
		return c.Reply(ctx, done)
	case "clear":
		if _, err := c.UpdateConfig(ctx, func(values *agent.ConfigValues) {
			values.PromptOverride = nil
		}); err != nil {
			return err
		}
		return c.Reply(ctx, "✅ Custom instructions deleted. I follow the main prompt only.")
	default:
		return c.Reply(ctx, fmt.Sprintf("Send /prompt to see this chat's custom instructions, /prompt set <text> to add to the main prompt, "+
			"/prompt replace <text> to use them instead of it, or /prompt clear. Up to %d bytes.", agent.MaxPromptBytes))
	}
}

// replyPrompt shows the custom instructions, with a button to clear them.
func replyPrompt(ctx context.Context, c *command.Context, prefix string, override *agent.PromptOverride) error {
	if override == nil {
		return c.Reply(ctx, prefix+formatPrompt(override)+"\n\nAdd some: /prompt set <text> (on top of the main prompt), or /prompt replace <text> (instead of it).")
	}
	text := prefix + formatPrompt(override) + ":\n" + override.Text + "\n\nChange: /prompt set <text> or /prompt replace <text>."
	return c.ReplyButtons(ctx, text, command.Button{Label: "Delete instructions", Args: "clear"})
}

// formatPrompt says whether the chat has custom instructions and how they
// are used.
func formatPrompt(override *agent.PromptOverride) string {
	switch {
	case override == nil:
		return "*Custom instructions*: none. I follow the main prompt."
	case override.Mode == agent.PromptReplace:
		return "*Custom instructions*, used instead of the main prompt"
	default:
		return "*Custom instructions*, added to the main prompt"
	}
}

// parsePromptArgs returns "view", "set" or "replace" with its text, "clear",
// or "" for invalid arguments.
func parsePromptArgs(args string, hasArgs bool) (action, text string) {
	switch {
	case !hasArgs || args == "view":
		return "view", ""
	case args == "clear":
		return "clear", ""
	}
	for _, action := range []string{"set", "replace"} {
		if value, ok := strings.CutPrefix(args, action+" "); ok && strings.TrimSpace(value) != "" && len(value) <= agent.MaxPromptBytes {
			return action, value
		}
	}
	return "", ""
}
