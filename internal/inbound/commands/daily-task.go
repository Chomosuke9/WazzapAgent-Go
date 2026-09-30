package commands

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Chomosuke9/DiscordAgent-Go/internal/agent"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/command"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/conversation"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/mention"
)

func init() {
	register(command.Command{
		Name: "daily-task",
		// Only the bot runs it: people ask the bot, and the bot decides.
		Permission: "fromMe",
		Description: "Runs a task in this chat every day at a set time. Format: /daily-task <HH:MM> <task>, " +
			"with a 24-hour time in the bot's time zone. Example: /daily-task 07:00 Say good morning to everyone. " +
			"/daily-task list shows the daily tasks; /daily-task delete <ID> deletes one.",
		DeniedReply: "Just ask me, for example: every day at 07:00, say good morning.",
		Run:         runDailyTask,
	})
}

func runDailyTask(ctx context.Context, c *command.Context) error {
	verb, rest, _ := strings.Cut(strings.TrimSpace(c.Args), " ")
	switch strings.ToLower(verb) {
	case "list":
		return replyDailyTasks(ctx, c)
	case "delete":
		return deleteDailyTask(ctx, c, strings.TrimSpace(rest))
	}
	minute, prompt, ok := parseDailyTaskArgs(c.Args)
	if !ok {
		return c.Reply(ctx, dailyTaskUsage())
	}
	task, err := c.ScheduleDailyTask(ctx, minute, dailyTaskMentions(prompt, c.Message.Mentions, c.AssistantName()))
	switch {
	case agent.IsCode(err, agent.ErrorResourceExhausted):
		return c.Reply(ctx, "This chat already has the most daily tasks allowed. Delete one first (/daily-task list).")
	case agent.IsCode(err, agent.ErrorInvalidArgument):
		return c.Reply(ctx, "The task is too long. Keep it under 4,000 characters.")
	case err != nil:
		return err
	}
	reply := fmt.Sprintf("Daily task added: every day at %02d:%02d.", minute/60, minute%60)
	if task.Code != "" {
		reply = fmt.Sprintf("Daily task added: every day at %s (%s). ID: %s (/daily-task delete %s stops it).",
			task.FireAt.Format("15:04"), dailyTaskZone(task.FireAt), task.Code, task.Code)
	}
	return c.Reply(ctx, reply)
}

var dailyTaskTimePattern = regexp.MustCompile(`^(\d{1,2})[:.](\d{2})$`)

// parseDailyTaskArgs splits "<HH:MM> <task>" into minutes after midnight
// and the task. "7:00" and "07.00" are accepted too.
func parseDailyTaskArgs(args string) (int, string, bool) {
	args = strings.TrimSpace(args)
	token, prompt := args, ""
	if index := strings.IndexFunc(args, unicode.IsSpace); index >= 0 {
		token, prompt = args[:index], strings.TrimSpace(args[index:])
	}
	match := dailyTaskTimePattern.FindStringSubmatch(token)
	if match == nil || prompt == "" {
		return 0, "", false
	}
	hour, _ := strconv.Atoi(match[1])
	minute, _ := strconv.Atoi(match[2])
	if hour > 23 || minute > 59 {
		return 0, "", false
	}
	return hour*60 + minute, prompt, true
}

func replyDailyTasks(ctx context.Context, c *command.Context) error {
	tasks, err := c.Tasks(ctx)
	if err != nil {
		return err
	}
	lines := []string{"Daily tasks:"}
	for _, task := range tasks {
		if task.Daily {
			lines = append(lines, "• "+task.Code+", every day at "+task.FireAt.Format("15:04")+" ("+dailyTaskZone(task.FireAt)+"): "+dailyTaskPreview(task.Prompt))
		}
	}
	if len(lines) == 1 {
		return c.Reply(ctx, "No daily tasks in this chat.")
	}
	return c.Reply(ctx, strings.Join(append(lines, "", "Delete one with /daily-task delete <ID>."), "\n"))
}

func deleteDailyTask(ctx context.Context, c *command.Context, code string) error {
	if code == "" {
		return c.Reply(ctx, "Usage: /daily-task delete <ID>. /daily-task list shows the IDs.")
	}
	deleted, err := c.CancelTask(ctx, code, true)
	if err != nil {
		return err
	}
	if !deleted {
		return c.Reply(ctx, "No daily task has the ID "+code+". /daily-task list shows the IDs.")
	}
	return c.Reply(ctx, "Daily task "+code+" deleted.")
}

// dailyTaskMentions rewrites the raw Discord mentions a person typed into
// the @Name (senderRef) form the model uses to tag someone.
func dailyTaskMentions(prompt string, bindings []conversation.MentionBinding, assistantName string) string {
	replacements := make(map[string]string, len(bindings))
	for _, binding := range bindings {
		if binding.Bot {
			replacements[binding.Token] = "@" + assistantName + " (Bot)"
		} else {
			replacements[binding.Token] = "@member (" + binding.SenderRef.String() + ")"
		}
	}
	return mention.Rewrite(prompt, replacements)
}

// dailyTaskZone writes the bot's time zone as a UTC offset.
func dailyTaskZone(at time.Time) string {
	if _, offset := at.Zone(); offset == 0 {
		return "UTC"
	}
	return "UTC" + at.Format("-07:00")
}

func dailyTaskPreview(prompt string) string {
	if runes := []rune(prompt); len(runes) > 80 {
		return string(runes[:79]) + "…"
	}
	return prompt
}

func dailyTaskUsage() string {
	return "Usage: /daily-task <HH:MM> <task>\n" +
		"The time is 24-hour, in the bot's time zone. Example: /daily-task 07:00 Say good morning to everyone.\n" +
		"/daily-task list shows the daily tasks; /daily-task delete <ID> deletes one."
}
