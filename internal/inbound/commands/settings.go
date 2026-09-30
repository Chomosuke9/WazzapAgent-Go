package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/Chomosuke9/DiscordAgent-Go/internal/agent"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/command"
)

func init() {
	register(command.Command{
		Name:        "settings",
		Aliases:     []string{"setting", "config"},
		Permission:  "(owner or (admin and group)) and !fromMe",
		Description: "Shows this chat's settings in one message, with a menu to change each part.",
		DeniedReply: "Only a group admin or the owner can see this chat's settings.",
		Run:         runSettings,
	})
}

// runSettings is the overview: the chat's current settings in a few lines,
// with a list menu for each part. Picking an option runs the command that
// changes it, which answers in plain text.
func runSettings(ctx context.Context, c *command.Context) error {
	if c.HasArgs {
		return c.Reply(ctx, "Send /settings on its own to see this chat's settings.")
	}
	lines := []string{"⚙️ *Chat settings*", "", "Current:"}
	var menus []command.Menu
	if c.Facts.IsGroup {
		triggers, level := c.Config.Triggers, c.Config.Permission.ModerationLevel
		lines = append(lines, "- When I reply: "+triggerSummary(triggers))
		if level.Valid() {
			lines = append(lines, fmt.Sprintf("- Moderation: level %d (%s)", level, strings.ToLower(moderationLevels[level].button)))
		}
		menus = append(menus, triggerMenu(triggers), moderationMenu(level))
	}
	lines = append(lines, "- Custom instructions: "+promptSummary(c.Config.PromptOverride))
	if tasks := settingsTasks(ctx, c); tasks != "" {
		lines = append(lines, "- Tasks: "+tasks)
	}
	menus = append(menus, command.Menu{Title: "More", Options: []command.Button{
		{Label: "Custom instructions", Description: "See or delete them", Command: "prompt"},
		{Label: "Reset chat", Description: "Clear my memory of this chat", Command: "reset"},
	}})
	return c.ReplyMenus(ctx, strings.Join(lines, "\n"), "Tap a menu to change a setting", menus...)
}

// triggerSummary names the triggers that are on and counts the smart rules.
func triggerSummary(triggers agent.TriggerConfig) string {
	var on []string
	for _, toggle := range triggerToggles {
		if triggerOn(triggers, toggle.name) {
			on = append(on, toggle.name)
		}
	}
	summary := "never (every trigger is off)"
	if len(on) > 0 {
		summary = strings.Join(on, ", ")
	}
	switch rules := len(triggers.SmartRuleList()); {
	case rules == 0:
	case triggers.Smart:
		summary += fmt.Sprintf(", %d smart %s", rules, plural(rules, "rule", "rules"))
	default:
		summary += fmt.Sprintf(", %d smart %s paused", rules, plural(rules, "rule", "rules"))
	}
	return summary
}

// triggerMenu flips one trigger per option; the last option shows the
// rules and how to change them.
func triggerMenu(triggers agent.TriggerConfig) command.Menu {
	menu := command.Menu{Title: "When I reply"}
	for _, toggle := range triggerToggles {
		option := command.Button{Command: "trigger", Label: toggle.label + ": off", Description: "Tap to turn it on", Args: toggle.name + " on"}
		if triggerOn(triggers, toggle.name) {
			option = command.Button{Command: "trigger", Label: toggle.label + ": on", Description: "Tap to turn it off", Args: toggle.name + " off"}
		}
		menu.Options = append(menu.Options, option)
	}
	menu.Options = append(menu.Options, command.Button{Command: "trigger", Label: "Smart rules and more", Description: "See the rules and how to change them"})
	return menu
}

func moderationMenu(current agent.ModerationLevel) command.Menu {
	menu := command.Menu{Title: "Moderation"}
	for level, described := range moderationLevels {
		label := fmt.Sprintf("Level %d: %s", level, strings.ToLower(described.button))
		if agent.ModerationLevel(level) == current {
			label = "✓ " + label
		}
		menu.Options = append(menu.Options, command.Button{Command: "permission", Label: label, Description: described.can, Args: fmt.Sprint(level)})
	}
	return menu
}

func promptSummary(override *agent.PromptOverride) string {
	switch {
	case override == nil:
		return "none"
	case override.Mode == agent.PromptReplace:
		return "set, used instead of the main prompt"
	default:
		return "set, added to the main prompt"
	}
}

// settingsTasks counts the chat's tasks, or is empty when the host has none.
// People cannot type the task commands; they ask the bot, which sees them.
func settingsTasks(ctx context.Context, c *command.Context) string {
	tasks, err := c.Tasks(ctx)
	if agent.IsCode(err, agent.ErrorUnavailable) {
		return ""
	}
	if err != nil {
		return "could not be read right now"
	}
	var once, daily int
	for _, task := range tasks {
		if task.Daily {
			daily++
		} else {
			once++
		}
	}
	if once+daily == 0 {
		return "none; ask me to remind you of something, once or every day"
	}
	return fmt.Sprintf("%d one-off, %d daily; ask me to list or change them", once, daily)
}

func plural(count int, one, many string) string {
	if count == 1 {
		return one
	}
	return many
}
