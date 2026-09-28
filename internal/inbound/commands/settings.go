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
		Name:        "settings",
		Aliases:     []string{"setting", "config"},
		Permission:  "(owner or (admin and group)) and !fromMe",
		Description: "Shows all of this chat's settings and tasks in one message, with buttons to change each part.",
		DeniedReply: "Only a group admin or the owner can see this chat's settings.",
		Run:         runSettings,
	})
}

// runSettings is the overview: one message with every setting, and a
// button that opens the command that changes each part.
func runSettings(ctx context.Context, c *command.Context) error {
	if c.HasArgs {
		return c.Reply(ctx, "Send /settings on its own to see this chat's settings.")
	}
	group := c.Facts.IsGroup
	lines := []string{"⚙️ *Settings for this chat*"}
	buttons := []command.Button{}
	if group {
		triggers := c.Config.Triggers
		lines = append(lines, "", "*When I reply*: "+strings.Join([]string{
			check(triggers.Mention) + " mention", check(triggers.Reply) + " reply", check(triggers.Name) + " name", check(triggers.Smart) + " smart",
		}, ", "))
		switch rules := triggers.SmartRuleList(); len(rules) {
		case 0:
		case 1:
			lines = append(lines, "1 smart rule; /trigger shows it.")
		default:
			lines = append(lines, fmt.Sprintf("%d smart rules; /trigger lists them.", len(rules)))
		}
		lines = append(lines, "", formatModeration(c.Config.Permission.ModerationLevel))
		buttons = append(buttons,
			command.Button{Label: "When I reply", Command: "trigger"},
			command.Button{Label: "Moderation", Command: "permission"})
	}
	lines = append(lines, "", formatPrompt(c.Config.PromptOverride))
	if override := c.Config.PromptOverride; override != nil {
		lines[len(lines)-1] += ": " + scheduleTaskPreview(strings.Join(strings.Fields(override.Text), " "))
	}
	buttons = append(buttons, command.Button{Label: "Custom instructions", Command: "prompt"})
	lines = append(lines, "", settingsTasks(ctx, c))
	lines = append(lines, "", "Tap a button to see or change a part. You can also ask me in plain words, for example \"remind us every day at 07:00 to post the report\".")
	return c.ReplyButtons(ctx, strings.Join(lines, "\n"), buttons...)
}

// settingsTasks lists the chat's tasks. People cannot type the task
// commands, so this and the app are where they see them.
func settingsTasks(ctx context.Context, c *command.Context) string {
	tasks, err := c.Tasks(ctx)
	if agent.IsCode(err, agent.ErrorUnavailable) {
		return "*Tasks*: not available here."
	}
	if err != nil {
		return "*Tasks*: could not be read right now."
	}
	if len(tasks) == 0 {
		return "*Tasks*: none. Ask me to remind you of something, once or every day."
	}
	lines := []string{"*Tasks* (ask me to add or delete one):"}
	for _, task := range tasks {
		if task.Daily {
			lines = append(lines, "• "+task.Code+", every day at "+task.FireAt.Format("15:04")+" ("+dailyTaskZone(task.FireAt)+"): "+dailyTaskPreview(task.Prompt))
		} else {
			lines = append(lines, "• "+task.Code+", "+task.FireAt.Format("2 Jan 15:04")+": "+scheduleTaskPreview(task.Prompt))
		}
	}
	return strings.Join(lines, "\n")
}
