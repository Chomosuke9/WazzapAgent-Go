package commands

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

type recordingTasks struct {
	key    agent.Key
	fireAt time.Time
	prompt string
	calls  int
}

func (tasks *recordingTasks) ScheduleTask(_ context.Context, key agent.Key, _ identity.MessageID, fireAt time.Time, prompt string) error {
	tasks.calls++
	tasks.key, tasks.fireAt, tasks.prompt = key, fireAt, prompt
	return nil
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
	registry := builtinRegistry(t)
	request, _, recognized := registry.Parse(text)
	if !recognized {
		t.Fatal("/schedule-task is not registered")
	}
	key := testChatKey(t)
	message.TenantID, message.AccountID, message.ChatID, message.Text = key.TenantID, key.AccountID, key.ChatID, text
	tasks, sent := &recordingTasks{}, &recordingText{}
	err := registry.Dispatch(context.Background(), request, command.Invocation{
		Message: message, Facts: facts,
		Platform: command.Platform{Text: sent, Tasks: tasks},
		Store:    &handledStore{},
	})
	return tasks, sent, err
}

func TestScheduleTaskSchedulesForTheOwner(t *testing.T) {
	ref, _ := identity.NewSenderRef()
	message := conversation.IncomingMessage{
		Mentions: []conversation.MentionBinding{{Token: "@15550000001", SenderRef: ref}},
	}
	before := time.Now()
	tasks, sent, err := dispatchScheduleTask(t, "/schedule-task 1H30M remind @15550000001 to pay", command.PermissionFacts{IsOwner: true, IsGroup: true}, message)
	if err != nil || tasks.calls != 1 || len(sent.sent) != 1 || sent.sent[0] != "Task scheduled in 1h 30m." {
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

func TestScheduleTaskIsOwnerOrBotOnly(t *testing.T) {
	tasks, _, err := dispatchScheduleTask(t, "/schedule-task 1H hi", command.PermissionFacts{IsAdmin: true, IsGroup: true}, conversation.IncomingMessage{})
	if !errors.Is(err, command.ErrDenied) || tasks.calls != 0 {
		t.Fatalf("admin: err=%v calls=%d", err, tasks.calls)
	}
}

func TestScheduleTaskCountsFromWhenTheMessageArrived(t *testing.T) {
	received := time.Now().Add(-2 * time.Hour)
	tasks, _, err := dispatchScheduleTask(t, "/schedule-task 1H late", command.PermissionFacts{IsOwner: true}, conversation.IncomingMessage{ReceivedAt: received})
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
