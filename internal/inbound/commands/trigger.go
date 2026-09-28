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
		Name:       "trigger",
		Permission: "(owner or isAdmin or fromMe) and isGroup",
		Description: "Configures when the Agent responds in this group: /trigger view, /trigger mention|name|reply|smart on|off, " +
			"/trigger smart add <rule>, /trigger smart remove <number>, /trigger smart set <rules, one per line>, /trigger smart clear, " +
			"/trigger regex on|off, /trigger pattern <regex>. Only the owner or a group admin can use it in a group.",
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
	field, value, _ := strings.Cut(strings.TrimSpace(c.Args), " ")
	if index, ok := smartRuleNumber(strings.TrimSpace(value)); field == "smart" && ok && index > len(c.Config.Triggers.SmartRuleList()) {
		return c.Reply(ctx, fmt.Sprintf("There is no smart rule %d. /trigger view lists the rules.", index))
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
	if field == "smart" {
		// Rules may start on the next line: "/trigger smart set\nrule 1\nrule 2".
		if rules, ok := strings.CutPrefix(value, "set"); ok && rules != "" && strings.TrimLeft(rules, " \t\r\n") != rules {
			rules = strings.TrimSpace(rules)
			if rules == "" {
				return nil, false
			}
			return func(triggers *agent.TriggerConfig) { triggers.Smart, triggers.SmartRules = true, rules }, true
		}
		if value == "clear" {
			return func(triggers *agent.TriggerConfig) { triggers.SmartRules = "" }, true
		}
		if rule, ok := strings.CutPrefix(value, "add "); ok && strings.TrimSpace(rule) != "" {
			// One rule per line, so a multi-line rule becomes one line.
			rule = strings.Join(strings.Fields(rule), " ")
			return func(triggers *agent.TriggerConfig) {
				triggers.Smart, triggers.SmartRules = true, strings.Join(append(triggers.SmartRuleList(), rule), "\n")
			}, true
		}
		if index, ok := smartRuleNumber(value); ok {
			return func(triggers *agent.TriggerConfig) {
				rules := triggers.SmartRuleList()
				if index <= len(rules) {
					triggers.SmartRules = strings.Join(append(rules[:index-1], rules[index:]...), "\n")
				}
			}, true
		}
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

// smartRuleNumber reads "remove <n>", the 1-based number of a smart rule.
func smartRuleNumber(value string) (int, bool) {
	number, ok := strings.CutPrefix(value, "remove ")
	if !ok {
		return 0, false
	}
	index, err := strconv.Atoi(strings.TrimSpace(number))
	return index, err == nil && index >= 1
}

func formatTriggers(triggers agent.TriggerConfig) string {
	nameDetail := "Agent name"
	if triggers.NameRegex {
		nameDetail = "regex: " + strconv.Quote(triggers.NamePattern)
	}
	text := fmt.Sprintf("Group triggers:\n• mention: %s\n• name: %s (%s)\n• reply to bot: %s\n• smart (TypeSafe judges if a message is worth a response): %s",
		onOff(triggers.Mention), onOff(triggers.Name), nameDetail, onOff(triggers.Reply), onOff(triggers.Smart))
	for index, rule := range triggers.SmartRuleList() {
		text += fmt.Sprintf("\n   rule %d: %s", index+1, rule)
	}
	return text
}

func onOff(enabled bool) string {
	if enabled {
		return "on"
	}
	return "off"
}

func triggerUsage() string {
	return fmt.Sprintf("Usage: /trigger view, /trigger mention on|off, /trigger name on|off, /trigger reply on|off, /trigger smart on|off, /trigger smart add <rule>, /trigger smart remove <number>, /trigger smart set <rules>, /trigger smart clear, /trigger regex on|off, or /trigger pattern <regex>. The pattern uses Go regex syntax and automatically enables the name trigger and regex mode; maximum length is %d bytes. Smart rules are one per line (at most %d), for example \"someone sends a scam link\"; a message matching one wakes the Agent, and the Agent sees the rules.", agent.MaxTriggerPatternBytes, agent.MaxSmartRules)
}
