package commands

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
)

func init() {
	register(command.Command{
		Name:        "trigger",
		Permission:  "(owner or isAdmin) and !fromMe and isGroup",
		Description: "Configures Agent triggers per chat. Only the owner or a group admin can use it in a group.",
		DeniedReply: "The /trigger command can only be used by the owner or an admin in a group.",
		Run:         runTrigger,
	})
}

func runTrigger(ctx context.Context, c *command.Context) error {
	if !c.HasArgs || c.Args == "view" {
		return replyTriggers(ctx, c, "", c.Config.Triggers)
	}
	change, ok := parseTriggerArgs(c.Args)
	if !ok {
		return c.Reply(ctx, triggerUsage())
	}
	triggers := c.Config.Triggers
	change(&triggers)
	if err := triggers.Validate(); err != nil {
		return c.Reply(ctx, triggerUsage())
	}
	updated, err := c.UpdateConfig(ctx, func(values *agent.ConfigValues) { change(&values.Triggers) })
	if err != nil {
		if agent.IsCode(err, agent.ErrorInvalidArgument) {
			return c.Reply(ctx, triggerUsage())
		}
		return err
	}
	return replyTriggers(ctx, c, "Chat triggers updated.\n", updated.Triggers)
}

// replyTriggers shows the triggers with one toggle button per on/off
// trigger. A tap arrives back here as "/trigger <name> on|off".
func replyTriggers(ctx context.Context, c *command.Context, prefix string, triggers agent.TriggerConfig) error {
	toggle := func(label, name string, enabled bool) command.Button {
		if enabled {
			return command.Button{Label: label + ": turn off", Args: name + " off"}
		}
		return command.Button{Label: label + ": turn on", Args: name + " on"}
	}
	return c.ReplyButtons(ctx, prefix+formatTriggers(triggers),
		toggle("Mention", "mention", triggers.Mention),
		toggle("Name", "name", triggers.Name),
		toggle("Reply", "reply", triggers.Reply),
		toggle("Smart", "smart", triggers.Smart),
	)
}

// parseTriggerArgs returns the change "<field> on|off" or "pattern <regex>"
// makes to the trigger config.
func parseTriggerArgs(args string) (func(*agent.TriggerConfig), bool) {
	field, value, hasValue := strings.Cut(strings.TrimSpace(args), " ")
	value = strings.TrimSpace(value)
	if !hasValue {
		return nil, false
	}
	if field == "pattern" {
		if value == "" || !utf8.ValidString(value) || len(value) > agent.MaxTriggerPatternBytes {
			return nil, false
		}
		return func(triggers *agent.TriggerConfig) {
			triggers.Name, triggers.NameRegex, triggers.NamePattern = true, true, value
		}, true
	}
	if value != "on" && value != "off" {
		return nil, false
	}
	enabled := value == "on"
	switch field {
	case "mention":
		return func(triggers *agent.TriggerConfig) { triggers.Mention = enabled }, true
	case "name":
		return func(triggers *agent.TriggerConfig) { triggers.Name = enabled }, true
	case "reply":
		return func(triggers *agent.TriggerConfig) { triggers.Reply = enabled }, true
	case "smart":
		return func(triggers *agent.TriggerConfig) { triggers.Smart = enabled }, true
	case "regex":
		return func(triggers *agent.TriggerConfig) {
			triggers.NameRegex = enabled
			if enabled {
				triggers.Name = true
			}
		}, true
	default:
		return nil, false
	}
}

func formatTriggers(triggers agent.TriggerConfig) string {
	nameDetail := "Agent name"
	if triggers.NameRegex {
		nameDetail = "regex: " + strconv.Quote(triggers.NamePattern)
	}
	return fmt.Sprintf("Group triggers:\n• mention: %s\n• name: %s (%s)\n• reply to bot: %s\n• smart (TypeSafe judges if a message is for the bot): %s",
		onOff(triggers.Mention), onOff(triggers.Name), nameDetail, onOff(triggers.Reply), onOff(triggers.Smart))
}

func onOff(enabled bool) string {
	if enabled {
		return "on"
	}
	return "off"
}

func triggerUsage() string {
	return fmt.Sprintf("Usage: /trigger view, /trigger mention on|off, /trigger name on|off, /trigger reply on|off, /trigger smart on|off, /trigger regex on|off, or /trigger pattern <regex>. The pattern uses Go regex syntax and automatically enables the name trigger and regex mode; maximum length is %d bytes.", agent.MaxTriggerPatternBytes)
}
