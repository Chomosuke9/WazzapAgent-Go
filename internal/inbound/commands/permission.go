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
		Name:        "permission",
		Aliases:     []string{"permissions", "moderation"},
		Permission:  "owner or (admin and group) or fromMe",
		Description: "Shows and sets what the Agent may do to moderate this group, with buttons: /permission, or /permission 0-3 (0 off, 1 delete, 2 delete and mute, 3 delete, mute and kick).",
		DeniedReply: "The /permission command can only be used by group admins or the owner.",
		Run:         runPermission,
	})
}

// moderationLevels describes each level, as buttons and in sentences.
var moderationLevels = [...]struct{ button, can string }{
	{"Off", "I don't moderate."},
	{"Delete", "I may delete messages."},
	{"Delete + mute", "I may delete messages and mute members."},
	{"Delete, mute, kick", "I may delete messages, mute members and remove them."},
}

func runPermission(ctx context.Context, c *command.Context) error {
	level, set, ok := parsePermissionArgs(c.Args, c.HasArgs)
	if !ok {
		return c.Reply(ctx, "Send /permission to see the levels with buttons, or /permission 0, 1, 2 or 3.")
	}
	if !set {
		return replyPermission(ctx, c, c.Config.Permission.ModerationLevel)
	}
	if _, err := c.UpdateConfig(ctx, func(values *agent.ConfigValues) {
		values.Permission.ModerationLevel = level
	}); err != nil {
		return err
	}
	return c.Reply(ctx, fmt.Sprintf("✅ Moderation is now level %d. %s", level, moderationLevels[level].can))
}

// replyPermission shows the level with one button per level. A tap arrives
// back here as "/permission <n>".
func replyPermission(ctx context.Context, c *command.Context, current agent.ModerationLevel) error {
	buttons := make([]command.Button, 0, len(moderationLevels))
	for level, described := range moderationLevels {
		label := described.button
		if agent.ModerationLevel(level) == current {
			label = "✓ " + label
		}
		buttons = append(buttons, command.Button{Label: label, Args: fmt.Sprint(level)})
	}
	text := formatModeration(current) + "\n\n" + strings.Join([]string{
		"0: off", "1: delete messages", "2: delete + mute", "3: delete + mute + kick",
	}, "\n") + "\n\nI act only when a group admin asks or a smart rule says so, and the bot account must be a group admin."
	return c.ReplyButtons(ctx, text, buttons...)
}

// parsePermissionArgs returns set=false for a view request.
func parsePermissionArgs(args string, hasArgs bool) (level agent.ModerationLevel, set bool, ok bool) {
	if !hasArgs || args == "view" {
		return 0, false, true
	}
	if arg := strings.TrimSpace(args); len(arg) == 1 && arg[0] >= '0' && arg[0] <= '3' {
		return agent.ModerationLevel(arg[0] - '0'), true, true
	}
	return 0, false, false
}

// formatModeration says what the level lets the Agent do.
func formatModeration(level agent.ModerationLevel) string {
	if !level.Valid() {
		return "*Moderation*: unknown level."
	}
	return fmt.Sprintf("*Moderation*: level %d of 3. %s", level, moderationLevels[level].can)
}
