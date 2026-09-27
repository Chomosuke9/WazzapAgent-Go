package commands

import (
	"context"
	"strings"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
)

func init() {
	register(command.Command{
		Name:        "permission",
		Aliases:     []string{"permissions"},
		Permission:  "(owner or (admin and group)) and !fromMe",
		Description: "Sets the moderation permission level from 0 to 3.",
		DeniedReply: "The /permission command can only be used by group admins or the owner.",
		Run:         runPermission,
	})
}

const permissionUsage = "Usage: /permission 0, 1, 2, or 3. Level 0: no moderation; 1: delete; 2: delete+mute; 3: delete+mute+kick."

func runPermission(ctx context.Context, c *command.Context) error {
	level, set, ok := parsePermissionArgs(c.Args, c.HasArgs)
	if !ok {
		return c.Reply(ctx, permissionUsage)
	}
	if !set {
		return c.Reply(ctx, formatModerationLevel(c.Config.Permission.ModerationLevel))
	}
	if _, err := c.UpdateConfig(ctx, func(values *agent.ConfigValues) {
		values.Permission.ModerationLevel = level
	}); err != nil {
		return err
	}
	return c.Reply(ctx, "Permission updated. "+formatModerationLevel(level))
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

func formatModerationLevel(level agent.ModerationLevel) string {
	labels := [...]string{
		"Level 0: moderation disabled.",
		"Level 1: delete.",
		"Level 2: delete dan mute.",
		"Level 3: delete, mute, dan kick.",
	}
	if !level.Valid() {
		return "Invalid permission level."
	}
	return labels[level]
}
