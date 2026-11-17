package commands

import (
	"context"
	"strings"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/subagent"
)

func init() {
	register(command.Command{
		Name:        "subagent",
		Aliases:     []string{"subagents"},
		Permission:  "owner and !fromMe",
		Description: "Turns the sub-agent on or off for this chat. The sub-agent lets the Agent hand complex tasks (files, code, the web) to an external WazzapSubAgents service: /subagent on, /subagent off, /subagent global on|off for every chat, /subagent default on|off for chats that have not set their own.",
		DeniedReply: "The /subagent command can only be used by the configured owner.",
		Run:         runSubagent,
	})
}

const subagentUsage = "Invalid. Use `/subagent on`, `/subagent off`, `/subagent global on/off`, or `/subagent default on/off`"

func runSubagent(ctx context.Context, c *command.Context) error {
	scope, enabled, set, ok := parseSubagentArgs(c.Args, c.HasArgs)
	if !ok {
		return c.Reply(ctx, subagentUsage)
	}
	control := c.Subagent()
	if !set {
		state := "OFF"
		if control != nil && control.Enabled(ctx, c.Key()) {
			state = "ON"
		}
		text := "Subagent: *" + state + "*\n\n" +
			"Enable subagent for this chat to allow the Agent to call sub-agents for complex tasks.\n\n" +
			"_/subagent on_ - enable subagent\n" +
			"_/subagent off_ - disable subagent\n" +
			"_/subagent global on/off_ - enable/disable for all chats\n" +
			"_/subagent default on/off_ - set for chats that haven't set their own"
		if control == nil {
			text += "\n\nThe sub-agent isn't configured yet: set its URL in Settings first."
		}
		return c.Reply(ctx, text)
	}
	if control == nil {
		if !enabled {
			return c.Reply(ctx, "Subagent is already off: no sub-agent service is configured.")
		}
		// Turning it on would do nothing until the service URL is set.
		return c.Reply(ctx, "The sub-agent isn't configured yet. Set the sub-agent URL (SUBAGENT_URL) in Settings and restart the Agent before enabling it.")
	}
	if err := control.SetEnabled(ctx, c.Key(), scope, enabled); err != nil {
		return err
	}
	text := "Subagent disabled"
	if enabled {
		text = "Subagent enabled"
	}
	switch scope {
	case subagent.ScopeGlobal:
		text += " for all chats"
	case subagent.ScopeDefault:
		text += " by default"
	}
	text += "."
	return c.Notice(ctx, command.Notice{Icon: "🤖", Kind: "Subagent", Change: command.NoticeChanged, Item: text, Text: "✅ " + text})
}

// parseSubagentArgs returns set=false for a view request.
func parseSubagentArgs(args string, hasArgs bool) (scope subagent.Scope, enabled, set, ok bool) {
	parts := strings.Fields(strings.ToLower(args))
	if !hasArgs || (len(parts) == 1 && parts[0] == "view") {
		return 0, false, false, true
	}
	scope = subagent.ScopeChat
	if len(parts) == 2 {
		switch parts[0] {
		case "global":
			scope = subagent.ScopeGlobal
		case "default":
			scope = subagent.ScopeDefault
		default:
			return 0, false, false, false
		}
		parts = parts[1:]
	}
	if len(parts) != 1 || (parts[0] != "on" && parts[0] != "off") {
		return 0, false, false, false
	}
	return scope, parts[0] == "on", true, true
}
