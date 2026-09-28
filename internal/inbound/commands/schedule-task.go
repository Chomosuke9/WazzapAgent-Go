package commands

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/mention"
)

// maxScheduleTaskDelay is the furthest ahead a task may be scheduled.
const maxScheduleTaskDelay = 24 * time.Hour

func init() {
	register(command.Command{
		Name: "schedule-task",
		// Anyone may set a reminder, and the bot may for anyone who asked. A
		// firing task has no requester, so it cannot schedule another task.
		// A task's turn can only reply, react and send stickers.
		Permission: "!fromMe or requester",
		Description: "Runs a task once, later, in this chat. Format: /schedule-task <duration> <task>. " +
			"The duration combines hours (H) and minutes (M), for example 2H30M, 2H, or 45M, up to 24H. " +
			"Example: /schedule-task 1H30M Remind @Budi (a1b2c3) about the meeting. " +
			"/schedule-task list shows the pending tasks; /schedule-task delete <ID> deletes one.",
		Run: runScheduleTask,
	})
}

func runScheduleTask(ctx context.Context, c *command.Context) error {
	verb, rest, _ := strings.Cut(strings.TrimSpace(c.Args), " ")
	switch strings.ToLower(verb) {
	case "list":
		return replyScheduledTasks(ctx, c)
	case "delete":
		return deleteScheduledTask(ctx, c, strings.TrimSpace(rest))
	}
	delay, prompt, ok := parseScheduleTaskArgs(c.Args)
	if !ok {
		return c.Reply(ctx, scheduleTaskUsage())
	}
	if delay > maxScheduleTaskDelay {
		return c.Reply(ctx, "A task can be scheduled at most 24 hours ahead.")
	}
	// Count from when the message arrived, so a command recovered after a
	// crash keeps its original time (and runs at once if already due).
	requested := c.Message.ReceivedAt
	if requested.IsZero() {
		requested = time.Now()
	}
	task, err := c.ScheduleTask(ctx, requested.Add(delay), scheduleTaskMentions(prompt, c.Message.Mentions, c.AssistantName()))
	if agent.IsCode(err, agent.ErrorInvalidArgument) {
		return c.Reply(ctx, "The task is too long. Keep it under 4,000 characters.")
	}
	if err != nil {
		return err
	}
	reply := "Task scheduled in " + formatScheduleTaskDelay(delay) + "."
	if task.Code != "" {
		reply += " ID: " + task.Code + " (/schedule-task delete " + task.Code + " cancels it)."
	}
	return c.Reply(ctx, reply)
}

func replyScheduledTasks(ctx context.Context, c *command.Context) error {
	tasks, err := c.Tasks(ctx)
	if err != nil {
		return err
	}
	lines := []string{"Scheduled tasks:"}
	for _, task := range tasks {
		if !task.Daily {
			lines = append(lines, "• "+task.Code+", "+task.FireAt.Format("2 Jan 15:04")+": "+scheduleTaskPreview(task.Prompt))
		}
	}
	if len(lines) == 1 {
		return c.Reply(ctx, "No scheduled tasks in this chat.")
	}
	return c.Reply(ctx, strings.Join(append(lines, "", "Delete one with /schedule-task delete <ID>."), "\n"))
}

func deleteScheduledTask(ctx context.Context, c *command.Context, code string) error {
	if code == "" {
		return c.Reply(ctx, "Usage: /schedule-task delete <ID>. /schedule-task list shows the IDs.")
	}
	deleted, err := c.CancelTask(ctx, code, false)
	if err != nil {
		return err
	}
	if !deleted {
		return c.Reply(ctx, "No scheduled task has the ID "+code+". /schedule-task list shows the IDs.")
	}
	return c.Reply(ctx, "Scheduled task "+code+" deleted.")
}

// scheduleTaskPreview shortens a task's prompt for a list.
func scheduleTaskPreview(prompt string) string {
	if runes := []rune(prompt); len(runes) > 80 {
		return string(runes[:79]) + "…"
	}
	return prompt
}

var scheduleTaskDurationPattern = regexp.MustCompile(`^(?i)(?:(\d{1,4})h)?(?:(\d{1,5})m)?$`)

// parseScheduleTaskArgs splits "<duration> <task>". The duration is hours
// and/or minutes such as 2H30M, 2H or 45m, and must be positive.
func parseScheduleTaskArgs(args string) (time.Duration, string, bool) {
	args = strings.TrimSpace(args)
	token, prompt := args, ""
	if index := strings.IndexFunc(args, unicode.IsSpace); index >= 0 {
		token, prompt = args[:index], strings.TrimSpace(args[index:])
	}
	match := scheduleTaskDurationPattern.FindStringSubmatch(token)
	if match == nil || (match[1] == "" && match[2] == "") || prompt == "" {
		return 0, "", false
	}
	hours, _ := strconv.Atoi("0" + match[1])
	minutes, _ := strconv.Atoi("0" + match[2])
	delay := time.Duration(hours)*time.Hour + time.Duration(minutes)*time.Minute
	if delay <= 0 {
		return 0, "", false
	}
	return delay, prompt, true
}

// scheduleTaskMentions rewrites the raw WhatsApp mentions a person typed
// into the @Name (senderRef) form the model uses to tag someone.
func scheduleTaskMentions(prompt string, bindings []conversation.MentionBinding, assistantName string) string {
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

func formatScheduleTaskDelay(delay time.Duration) string {
	hours, minutes := int(delay/time.Hour), int(delay%time.Hour/time.Minute)
	parts := make([]string, 0, 2)
	if hours > 0 {
		parts = append(parts, strconv.Itoa(hours)+"h")
	}
	if minutes > 0 {
		parts = append(parts, strconv.Itoa(minutes)+"m")
	}
	return strings.Join(parts, " ")
}

func scheduleTaskUsage() string {
	return "Usage: /schedule-task <duration> <task>\n" +
		"Duration: hours (H) and minutes (M), for example 2H30M, 2H, or 45M. At most 24H.\n" +
		"Example: /schedule-task 1H30M Remind everyone about the meeting.\n" +
		"/schedule-task list shows the pending tasks; /schedule-task delete <ID> deletes one."
}
