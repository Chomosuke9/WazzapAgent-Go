package commands

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Chomosuke9/DiscordAgent-Go/internal/action"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/agent"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/command"
)

func init() {
	register(command.Command{
		Name:       "trigger",
		Permission: "(owner or isAdmin or fromMe) and isGroup",
		Description: "Shows and changes when the Agent replies in this group: /trigger shows it and how to change it; " +
			"/trigger toggle has buttons; /trigger mention|name|reply|smart on|off; /trigger smart add <rule>, " +
			"/trigger smart remove <number>, /trigger smart set <rules, one per line>, /trigger smart clear; " +
			"/trigger pattern <regex>, /trigger regex off. Only the owner or a group admin can use it in a group.",
		DeniedReply: "The /trigger command can only be used by the owner or an admin in a group.",
		Run:         runTrigger,
	})
}

// triggerToggles are the on/off triggers, in the order /trigger shows them.
var triggerToggles = []struct{ name, label string }{
	{"mention", "Mention"}, {"reply", "Reply"}, {"name", "Name"}, {"smart", "Smart"},
}

func runTrigger(ctx context.Context, c *command.Context) error {
	args := strings.TrimSpace(c.Args)
	switch args {
	case "", "view", "help", "smart", "smart view":
		return c.Reply(ctx, formatTriggers(c.Config.Triggers, c.AssistantName())+"\n\n"+triggerUsage)
	case "toggle":
		return replyTriggerToggles(ctx, c, c.Config.Triggers)
	}
	change, done, ok := parseTriggerArgs(args)
	if !ok {
		return c.Reply(ctx, triggerHelp(args))
	}
	if index, ok := smartRuleNumber(strings.TrimPrefix(args, "smart ")); strings.HasPrefix(args, "smart ") && ok && index > len(c.Config.Triggers.SmartRuleList()) {
		return c.Reply(ctx, fmt.Sprintf("There is no smart rule %d. Send /trigger to see the rules.", index))
	}
	triggers := c.Config.Triggers
	change(&triggers)
	if err := triggers.Validate(); err != nil {
		return c.Reply(ctx, triggerInvalid(triggers))
	}
	updated, err := c.UpdateConfig(ctx, func(values *agent.ConfigValues) { change(&values.Triggers) })
	if err != nil {
		if agent.IsCode(err, agent.ErrorInvalidArgument) {
			return c.Reply(ctx, triggerInvalid(triggers))
		}
		return err
	}
	return c.Reply(ctx, "✅ "+done+"\n\n"+formatTriggers(updated.Triggers, c.AssistantName()))
}

// replyTriggerToggles shows the triggers with a toggle button per trigger and
// a remove button per smart rule. A tap arrives back here as
// "/trigger <name> on|off" or "/trigger smart remove <n>", and its answer is
// plain text; /trigger toggle sends the buttons again.
func replyTriggerToggles(ctx context.Context, c *command.Context, triggers agent.TriggerConfig) error {
	buttons := make([]command.Button, 0, action.MaxButtons)
	for _, toggle := range triggerToggles {
		if triggerOn(triggers, toggle.name) {
			buttons = append(buttons, command.Button{Label: "Turn off " + strings.ToLower(toggle.label), Args: toggle.name + " off"})
		} else {
			buttons = append(buttons, command.Button{Label: "Turn on " + strings.ToLower(toggle.label), Args: toggle.name + " on"})
		}
	}
	for index := range triggers.SmartRuleList() {
		if len(buttons) == action.MaxButtons {
			break
		}
		buttons = append(buttons, command.Button{Label: fmt.Sprintf("Remove rule %d", index+1), Args: fmt.Sprintf("smart remove %d", index+1)})
	}
	return c.ReplyButtons(ctx, formatTriggers(triggers, c.AssistantName())+"\n\nTap to turn one on or off, or to remove a rule.", buttons...)
}

func triggerOn(triggers agent.TriggerConfig, name string) bool {
	switch name {
	case "mention":
		return triggers.Mention
	case "reply":
		return triggers.Reply
	case "name":
		return triggers.Name
	default:
		return triggers.Smart
	}
}

// parseTriggerArgs returns the change the arguments make to the trigger
// config and a sentence saying what it did.
func parseTriggerArgs(args string) (change func(*agent.TriggerConfig), done string, ok bool) {
	field, value, hasValue := strings.Cut(strings.TrimSpace(args), " ")
	value = strings.TrimSpace(value)
	if !hasValue {
		return nil, "", false
	}
	if field == "smart" {
		// Rules may start on the next line: "/trigger smart set\nrule 1\nrule 2".
		if rules, ok := strings.CutPrefix(value, "set"); ok && rules != "" && strings.TrimLeft(rules, " \t\r\n") != rules {
			rules = strings.TrimSpace(rules)
			if rules == "" {
				return nil, "", false
			}
			return func(triggers *agent.TriggerConfig) { triggers.Smart, triggers.SmartRules = true, rules }, "Smart rules replaced, and Smart is on.", true
		}
		if value == "clear" {
			return func(triggers *agent.TriggerConfig) { triggers.SmartRules = "" }, "All smart rules deleted.", true
		}
		if rule, ok := strings.CutPrefix(value, "add "); ok && strings.TrimSpace(rule) != "" {
			// One rule per line, so a multi-line rule becomes one line.
			rule = strings.Join(strings.Fields(rule), " ")
			return func(triggers *agent.TriggerConfig) {
				triggers.Smart, triggers.SmartRules = true, strings.Join(append(triggers.SmartRuleList(), rule), "\n")
			}, "Smart rule added, and Smart is on.", true
		}
		if index, ok := smartRuleNumber(value); ok {
			return func(triggers *agent.TriggerConfig) {
				rules := triggers.SmartRuleList()
				if index <= len(rules) {
					triggers.SmartRules = strings.Join(append(rules[:index-1], rules[index:]...), "\n")
				}
			}, fmt.Sprintf("Smart rule %d removed.", index), true
		}
	}
	if field == "pattern" {
		if value == "" || !utf8.ValidString(value) || len(value) > agent.MaxTriggerPatternBytes {
			return nil, "", false
		}
		return func(triggers *agent.TriggerConfig) {
			triggers.Name, triggers.NameRegex, triggers.NamePattern = true, true, value
		}, "The name trigger now uses your regex.", true
	}
	if value != "on" && value != "off" {
		return nil, "", false
	}
	enabled := value == "on"
	switch field {
	case "mention":
		return func(triggers *agent.TriggerConfig) { triggers.Mention = enabled }, "Mention is " + value + ".", true
	case "name":
		return func(triggers *agent.TriggerConfig) { triggers.Name = enabled }, "Name is " + value + ".", true
	case "reply":
		return func(triggers *agent.TriggerConfig) { triggers.Reply = enabled }, "Reply is " + value + ".", true
	case "smart":
		return func(triggers *agent.TriggerConfig) { triggers.Smart = enabled }, "Smart is " + value + ".", true
	case "regex":
		done := "The name trigger uses my name again."
		if enabled {
			done = "The name trigger uses your regex."
		}
		return func(triggers *agent.TriggerConfig) {
			triggers.NameRegex = enabled
			if enabled {
				triggers.Name = true
			}
		}, done, true
	default:
		return nil, "", false
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

// formatTriggers says, in plain words, when the Agent replies in the group
// and lists the smart rules.
func formatTriggers(triggers agent.TriggerConfig, assistantName string) string {
	name := "my name"
	if assistantName = strings.TrimSpace(assistantName); assistantName != "" {
		name = strconv.Quote(assistantName)
	}
	nameDetail := "someone writes " + name
	if triggers.NameRegex {
		nameDetail = "a message matches the regex " + triggers.NamePattern
	}
	lines := []string{
		"*When I reply in this group*",
		check(triggers.Mention) + " Mention: someone @mentions me",
		check(triggers.Reply) + " Reply: someone replies to my message",
		check(triggers.Name) + " Name: " + nameDetail,
		check(triggers.Smart) + " Smart: I read the other messages and reply when one is meant for me",
		"",
	}
	rules := triggers.SmartRuleList()
	switch {
	case len(rules) == 0:
		lines = append(lines, "*Smart rules*: none yet.")
	case triggers.Smart:
		lines = append(lines, fmt.Sprintf("*Smart rules* (%d of %d). A message that matches one always wakes me, and I follow the rule:", len(rules), agent.MaxSmartRules))
	default:
		lines = append(lines, fmt.Sprintf("*Smart rules* (%d of %d), paused while Smart is off:", len(rules), agent.MaxSmartRules))
	}
	for index, rule := range rules {
		lines = append(lines, fmt.Sprintf("%d. %s", index+1, rule))
	}
	return strings.Join(lines, "\n")
}

// triggerUsage is how to change the triggers, shown under /trigger.
const triggerUsage = "*How to change it*\n" +
	"/trigger toggle: buttons to turn each one on or off\n" +
	"/trigger mention|reply|name|smart on|off\n" +
	"/trigger smart add <rule>: add a rule, e.g. /trigger smart add someone sends a scam link: delete it and warn them\n" +
	"/trigger smart remove <number>: delete a rule\n" +
	"/trigger smart set <rules, one per line>: replace all rules\n" +
	"/trigger smart clear: delete all rules\n" +
	"/trigger pattern <regex>: match the name with a regex; /trigger regex off goes back to my name"

func check(enabled bool) string {
	if enabled {
		return "✅"
	}
	return "❌"
}

// triggerHelp answers arguments /trigger does not understand with the
// forms that start the same way.
func triggerHelp(args string) string {
	field, _, _ := strings.Cut(args, " ")
	switch field {
	case "smart":
		return "To manage smart rules, send one of:\n/trigger smart on|off\n/trigger smart add <rule>\n/trigger smart remove <number>\n" +
			"/trigger smart set <rules, one per line>\n/trigger smart clear\n\n" +
			"A rule says when it applies and what to do, e.g. someone sends a scam link: delete it and warn them."
	case "pattern", "regex":
		return fmt.Sprintf("Send /trigger pattern <regex> (Go syntax, up to %d bytes; add (?i) to ignore case), or /trigger regex off to use my name again.", agent.MaxTriggerPatternBytes)
	case "mention", "reply", "name":
		return "Send /trigger " + field + " on or /trigger " + field + " off."
	}
	return "I don't know that one.\n\n" + triggerUsage
}

// triggerInvalid explains why a change was refused.
func triggerInvalid(triggers agent.TriggerConfig) string {
	if len(triggers.SmartRuleList()) > agent.MaxSmartRules || len(triggers.SmartRules) > agent.MaxSmartRulesBytes {
		return fmt.Sprintf("A group can have at most %d smart rules (%d characters in all). Remove one first with /trigger smart remove <number>.", agent.MaxSmartRules, agent.MaxSmartRulesBytes)
	}
	if triggers.NameRegex && strings.TrimSpace(triggers.NamePattern) == "" {
		return "There is no regex yet. Send /trigger pattern <regex> to set one."
	}
	if triggers.NameRegex {
		return "That regex is not valid Go syntax. Try again with /trigger pattern <regex>, or /trigger regex off to use my name."
	}
	return triggerHelp("")
}
