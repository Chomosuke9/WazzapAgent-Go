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
		// Only the owner or the bot itself: the task later runs as a trusted
		// system turn, so anyone else could plant instructions in it.
		Permission: "fromMe or owner",
		Description: "Runs a task later in this chat. Format: /schedule-task <duration> <task>. " +
			"The duration combines hours (H) and minutes (M), for example 2H30M, 2H, or 45M, up to 24H. " +
			"Example: /schedule-task 1H30M Remind @Budi (a1b2c3) about the meeting.",
		DeniedReply: "The /schedule-task command can only be used by the owner.",
		Run:         runScheduleTask,
	})
}

func runScheduleTask(ctx context.Context, c *command.Context) error {
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
	err := c.ScheduleTask(ctx, requested.Add(delay), scheduleTaskMentions(prompt, c.Message.Mentions, c.AssistantName()))
	if agent.IsCode(err, agent.ErrorInvalidArgument) {
		return c.Reply(ctx, "The task is too long. Keep it under 4,000 characters.")
	}
	if err != nil {
		return err
	}
	return c.Reply(ctx, "Task scheduled in "+formatScheduleTaskDelay(delay)+".")
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
		"Example: /schedule-task 1H30M Remind everyone about the meeting."
}
