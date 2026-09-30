package commands

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Chomosuke9/DiscordAgent-Go/internal/agent"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/command"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/conversation"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/identity"
)

type recordingTasks struct {
	key    agent.Key
	fireAt time.Time
	minute int
	prompt string
	calls  int
	// saved are what Tasks lists; cancelled records CancelTask calls.
	saved     []command.Task
	cancelled []string
}

func (tasks *recordingTasks) ScheduleTask(_ context.Context, key agent.Key, _ identity.MessageID, fireAt time.Time, prompt string) (command.Task, error) {
	tasks.calls++
	tasks.key, tasks.fireAt, tasks.prompt = key, fireAt, prompt
	return command.Task{Code: "a1b2c3", Prompt: prompt, FireAt: fireAt}, nil
}

func (tasks *recordingTasks) ScheduleDailyTask(_ context.Context, key agent.Key, _ identity.MessageID, minute int, prompt string) (command.Task, error) {
	tasks.calls++
	tasks.key, tasks.minute, tasks.prompt = key, minute, prompt
	zone := time.FixedZone("", 7*60*60)
	fireAt := time.Date(2026, 9, 29, minute/60, minute%60, 0, 0, zone)
	return command.Task{Code: "d4e5f6", Prompt: prompt, FireAt: fireAt, Daily: true}, nil
}

func (tasks *recordingTasks) Tasks(context.Context, agent.Key) ([]command.Task, error) {
	return tasks.saved, nil
}

func (tasks *recordingTasks) CancelTask(_ context.Context, _ agent.Key, code string, daily bool) (bool, error) {
	tasks.cancelled = append(tasks.cancelled, fmt.Sprintf("%s daily=%v", code, daily))
	for _, task := range tasks.saved {
		if task.Code == code && task.Daily == daily {
			return true, nil
		}
	}
	return false, nil
}

func TestParseScheduleTaskArgs(t *testing.T) {
	cases := []struct {
		args   string
		delay  time.Duration
		prompt string
		ok     bool
	}{
		{"2H30M remind me", 2*time.Hour + 30*time.Minute, "remind me", true},
		{"2h say hi", 2 * time.Hour, "say hi", true},
		{"45m  say hi ", 45 * time.Minute, "say hi", true},
		{"90M\nsay hi", 90 * time.Minute, "say hi", true},
		{"25H too late", 25 * time.Hour, "too late", true}, // rejected by Run, not the parser
		{"0M nothing", 0, "", false},
		{"2H", 0, "", false},
		{"30 minutes", 0, "", false},
		{"M30 x", 0, "", false},
		{"", 0, "", false},
	}
	for _, test := range cases {
		delay, prompt, ok := parseScheduleTaskArgs(test.args)
		if delay != test.delay || prompt != test.prompt || ok != test.ok {
			t.Errorf("parseScheduleTaskArgs(%q) = %v, %q, %v", test.args, delay, prompt, ok)
		}
	}
}

func dispatchScheduleTask(t *testing.T, text string, facts command.PermissionFacts, message conversation.IncomingMessage) (*recordingTasks, *recordingText, error) {
	t.Helper()
	return dispatchTaskCommand(t, &recordingTasks{}, text, facts, message)
}

func dispatchTaskCommand(t *testing.T, tasks *recordingTasks, text string, facts command.PermissionFacts, message conversation.IncomingMessage) (*recordingTasks, *recordingText, error) {
	t.Helper()
	registry := builtinRegistry(t)
	request, _, recognized := registry.Parse(text)
	if !recognized {
		t.Fatalf("%s is not registered", text)
	}
	key := testChatKey(t)
	message.TenantID, message.AccountID, message.ChatID, message.Text = key.TenantID, key.AccountID, key.ChatID, text
	sent := &recordingText{}
	err := registry.Dispatch(context.Background(), request, command.Invocation{
		Message: message, Facts: facts,
		Platform: command.Platform{Text: sent, Tasks: tasks},
		Store:    &handledStore{},
	})
	return tasks, sent, err
}

func TestScheduleTaskSchedulesForTheBot(t *testing.T) {
	ref, _ := identity.NewSenderRef()
	message := conversation.IncomingMessage{
		Mentions: []conversation.MentionBinding{{Token: "@15550000001", SenderRef: ref}},
	}
	before := time.Now()
	tasks, sent, err := dispatchScheduleTask(t, "/schedule-task 1H30M remind @15550000001 to pay", command.PermissionFacts{FromMe: true, IsGroup: true}, message)
	if err != nil || tasks.calls != 1 || len(sent.sent) != 1 || sent.sent[0] != "Task scheduled in 1h 30m. ID: a1b2c3 (/schedule-task delete a1b2c3 cancels it)." {
		t.Fatalf("err=%v calls=%d sent=%q", err, tasks.calls, sent.sent)
	}
	if want := "remind @member (" + ref.String() + ") to pay"; tasks.prompt != want {
		t.Fatalf("prompt = %q, want %q", tasks.prompt, want)
	}
	if delay := tasks.fireAt.Sub(before); delay < 90*time.Minute || delay > 91*time.Minute {
		t.Fatalf("fire time is %s ahead", delay)
	}
}

func TestScheduleTaskRefusesMoreThanADay(t *testing.T) {
	tasks, sent, err := dispatchScheduleTask(t, "/schedule-task 24H1M too late", command.PermissionFacts{FromMe: true}, conversation.IncomingMessage{})
	if err != nil || tasks.calls != 0 || len(sent.sent) != 1 || sent.sent[0] != "A task can be scheduled at most 24 hours ahead." {
		t.Fatalf("err=%v calls=%d sent=%q", err, tasks.calls, sent.sent)
	}
	tasks, _, err = dispatchScheduleTask(t, "/schedule-task 24H just in time", command.PermissionFacts{FromMe: true}, conversation.IncomingMessage{})
	if err != nil || tasks.calls != 1 {
		t.Fatalf("24H: err=%v calls=%d", err, tasks.calls)
	}
}

func TestTaskCommandsAreBotOnly(t *testing.T) {
	for _, text := range []string{"/schedule-task 1H hi", "/daily-task 07:00 hi"} {
		// The inbound handler sends DeniedReply to a person who types it.
		if _, cmd, _ := builtinRegistry(t).Parse(text); !strings.HasPrefix(cmd.DeniedReply, "Just ask me") {
			t.Errorf("%s denied reply = %q", text, cmd.DeniedReply)
		}
		for _, test := range []struct {
			who   string
			facts command.PermissionFacts
			want  bool
		}{
			{"owner", command.PermissionFacts{IsOwner: true, IsPrivate: true}, false},
			{"group admin", command.PermissionFacts{IsAdmin: true, IsGroup: true}, false},
			{"member", command.PermissionFacts{IsGroup: true}, false},
			{"bot", command.PermissionFacts{FromMe: true, IsGroup: true}, true},
		} {
			tasks, sent, err := dispatchTaskCommand(t, &recordingTasks{}, text, test.facts, conversation.IncomingMessage{})
			allowed := !errors.Is(err, command.ErrDenied)
			if allowed != test.want || (allowed && (err != nil || tasks.calls != 1)) || (!allowed && tasks.calls != 0) {
				t.Errorf("%s by %s: err=%v calls=%d sent=%q, want allowed=%v", text, test.who, err, tasks.calls, sent.sent, test.want)
			}
		}
	}
}

func TestTaskCommandsListAndDeleteTheirOwnKind(t *testing.T) {
	owner := command.PermissionFacts{FromMe: true, IsGroup: true}
	zone := time.FixedZone("", 7*60*60)
	saved := []command.Task{
		{Code: "a1b2c3", Prompt: "remind about the meeting", FireAt: time.Date(2026, 9, 28, 20, 30, 0, 0, zone)},
		{Code: "d4e5f6", Prompt: "say good morning", FireAt: time.Date(2026, 9, 29, 7, 0, 0, 0, zone), Daily: true},
	}
	_, sent, err := dispatchTaskCommand(t, &recordingTasks{saved: saved}, "/schedule-task list", owner, conversation.IncomingMessage{})
	if err != nil || len(sent.sent) != 1 || sent.sent[0] != "Scheduled tasks:\n• a1b2c3, 28 Sep 20:30: remind about the meeting\n\nDelete one with /schedule-task delete <ID>." {
		t.Fatalf("list: err=%v sent=%q", err, sent.sent)
	}
	_, sent, err = dispatchTaskCommand(t, &recordingTasks{saved: saved}, "/daily-task list", owner, conversation.IncomingMessage{})
	if err != nil || len(sent.sent) != 1 || sent.sent[0] != "Daily tasks:\n• d4e5f6, every day at 07:00 (UTC+07:00): say good morning\n\nDelete one with /daily-task delete <ID>." {
		t.Fatalf("daily list: err=%v sent=%q", err, sent.sent)
	}
	tasks, sent, err := dispatchTaskCommand(t, &recordingTasks{saved: saved}, "/daily-task delete d4e5f6", owner, conversation.IncomingMessage{})
	if err != nil || len(tasks.cancelled) != 1 || tasks.cancelled[0] != "d4e5f6 daily=true" || sent.sent[0] != "Daily task d4e5f6 deleted." {
		t.Fatalf("daily delete: err=%v cancelled=%q sent=%q", err, tasks.cancelled, sent.sent)
	}
	// A daily task's ID does not delete it through /schedule-task.
	_, sent, err = dispatchTaskCommand(t, &recordingTasks{saved: saved}, "/schedule-task delete d4e5f6", owner, conversation.IncomingMessage{})
	if err != nil || sent.sent[0] != "No scheduled task has the ID d4e5f6. /schedule-task list shows the IDs." {
		t.Fatalf("cross delete: err=%v sent=%q", err, sent.sent)
	}
	_, sent, _ = dispatchTaskCommand(t, &recordingTasks{}, "/daily-task list", owner, conversation.IncomingMessage{})
	if sent.sent[0] != "No daily tasks in this chat." {
		t.Fatalf("empty daily list = %q", sent.sent)
	}
}

func TestDailyTaskSchedulesAtTheGivenTime(t *testing.T) {
	tasks, sent, err := dispatchTaskCommand(t, &recordingTasks{}, "/daily-task 7.05 say good morning", command.PermissionFacts{FromMe: true, IsGroup: true}, conversation.IncomingMessage{})
	if err != nil || tasks.minute != 7*60+5 || tasks.prompt != "say good morning" {
		t.Fatalf("err=%v minute=%d prompt=%q", err, tasks.minute, tasks.prompt)
	}
	if want := "Daily task added: every day at 07:05 (UTC+07:00). ID: d4e5f6 (/daily-task delete d4e5f6 stops it)."; sent.sent[0] != want {
		t.Fatalf("reply = %q, want %q", sent.sent[0], want)
	}
}

func TestParseDailyTaskArgs(t *testing.T) {
	for _, test := range []struct {
		args   string
		minute int
		prompt string
		ok     bool
	}{
		{"07:00 say hi", 7 * 60, "say hi", true},
		{"7:30 say hi", 7*60 + 30, "say hi", true},
		{"23.59\nsay hi", 23*60 + 59, "say hi", true},
		{"00:00 midnight", 0, "midnight", true},
		{"24:00 too late", 0, "", false},
		{"12:60 bad", 0, "", false},
		{"12:00", 0, "", false},
		{"7am say hi", 0, "", false},
	} {
		minute, prompt, ok := parseDailyTaskArgs(test.args)
		if minute != test.minute || prompt != test.prompt || ok != test.ok {
			t.Errorf("parseDailyTaskArgs(%q) = %d, %q, %v", test.args, minute, prompt, ok)
		}
	}
}

func TestScheduleTaskCountsFromWhenTheMessageArrived(t *testing.T) {
	received := time.Now().Add(-2 * time.Hour)
	tasks, _, err := dispatchScheduleTask(t, "/schedule-task 1H late", command.PermissionFacts{FromMe: true}, conversation.IncomingMessage{ReceivedAt: received})
	if err != nil || !tasks.fireAt.Equal(received.Add(time.Hour)) {
		t.Fatalf("err=%v fireAt=%v, want %v", err, tasks.fireAt, received.Add(time.Hour))
	}
}

func TestScheduleTaskMentionsUsesAssistantNameForBot(t *testing.T) {
	got := scheduleTaskMentions("Ping @628111 later", []conversation.MentionBinding{{Token: "@628111", Bot: true}}, "Vivy")
	if want := "Ping @Vivy (Bot) later"; got != want {
		t.Fatalf("scheduleTaskMentions = %q, want %q", got, want)
	}
}
