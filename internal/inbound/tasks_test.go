package inbound_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/inbound"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/sticker"
)

// startTaskChat sends one ordinary message so the chat exists, and returns
// its key and that message's ID.
func startTaskChat(t *testing.T, fixture *fixture, chat string) (agent.Key, identity.MessageID) {
	t.Helper()
	first := fixture.candidate("task-chat-"+chat, chat, conversation.ChatDirect, "hello")
	if err := fixture.handler.Handle(context.Background(), first); err != nil {
		t.Fatalf("handle first message: %v", err)
	}
	claimed, err := fixture.store.Inbound().ClaimAndResolveSender(context.Background(), first)
	if err != nil {
		t.Fatalf("reload first message: %v", err)
	}
	message := claimed.Message
	return agent.Key{TenantID: message.TenantID, AccountID: message.AccountID, ChatID: message.ChatID}, message.ID
}

func TestScheduledTaskRunsOnceAsASystemTurn(t *testing.T) {
	fixture := newFixture(t)
	key, source := startTaskChat(t, fixture, "15550000031@s.whatsapp.net")
	dispatcher := fixture.handler.dispatcher
	fireAt := time.Now().Add(50 * time.Millisecond)
	if _, err := dispatcher.ScheduleTask(context.Background(), key, source, fireAt, "remind 【everyone】 about the meeting"); err != nil {
		t.Fatalf("schedule task: %v", err)
	}
	// The same source message schedules one task only.
	if duplicate, err := dispatcher.ScheduleTask(context.Background(), key, source, fireAt, "remind everyone about the meeting"); err != nil || duplicate.Code != "" {
		t.Fatalf("schedule duplicate task: %v, %+v", err, duplicate)
	}
	dispatcher.WaitTasks()
	if err := fixture.handler.settle(nil); err != nil {
		t.Fatalf("task turn: %v", err)
	}
	if fixture.model.calls.Load() != 2 || fixture.sender.count() != 2 {
		t.Fatalf("model/sender calls = %d/%d, want 2/2", fixture.model.calls.Load(), fixture.sender.count())
	}
	transcript := fixture.model.lastRequest().Messages[4].Content
	if !strings.Contains(transcript, "reply: hello") ||
		!strings.Contains(transcript, "SYSTEM: Scheduled task firing now.") ||
		!strings.Contains(transcript, "Task: remind (everyone) about the meeting") {
		t.Fatalf("task transcript = %s", transcript)
	}
	if !strings.HasPrefix(fixture.sender.last().Text, "reply: Scheduled task firing now.") {
		t.Fatalf("sent text = %q", fixture.sender.last().Text)
	}
	tasks, err := fixture.store.Inbound().ListScheduledTasks(context.Background(), key.TenantID)
	if err != nil || len(tasks) != 0 {
		t.Fatalf("tasks after firing = %v, %v", tasks, err)
	}
}

func TestScheduledTaskOffersTheChatStickers(t *testing.T) {
	fixture := newFixture(t)
	key, source := startTaskChat(t, fixture, "15550000034@s.whatsapp.net")
	if _, err := fixture.store.Stickers().SaveSticker(context.Background(), key, sticker.Sticker{Name: "wave", WebP: []byte("webp")}); err != nil {
		t.Fatal(err)
	}
	dispatcher := fixture.handler.dispatcher
	if _, err := dispatcher.ScheduleTask(context.Background(), key, source, time.Now().Add(20*time.Millisecond), "send the wave sticker"); err != nil {
		t.Fatalf("schedule task: %v", err)
	}
	dispatcher.WaitTasks()
	if err := fixture.handler.settle(nil); err != nil {
		t.Fatalf("task turn: %v", err)
	}
	if got := fixture.model.lastRequest().Stickers; len(got) != 1 || got[0] != "wave" {
		t.Fatalf("task turn stickers = %v, want [wave]", got)
	}
}

func TestScheduledTaskRejectsMoreThanADayAhead(t *testing.T) {
	fixture := newFixture(t)
	key, source := startTaskChat(t, fixture, "15550000032@s.whatsapp.net")
	_, err := fixture.handler.dispatcher.ScheduleTask(context.Background(), key, source, time.Now().Add(25*time.Hour), "too late")
	if !agent.IsCode(err, agent.ErrorInvalidArgument) {
		t.Fatalf("err = %v, want invalid argument", err)
	}
}

func TestScheduledTaskSurvivesARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restart-task.db")
	tenantID, _ := identity.NewTenantID()
	accountID, _ := identity.NewAccountID()
	first := newFixtureAtPath(t, path, tenantID, accountID, 0, 1)
	key, source := startTaskChat(t, first, "15550000033@s.whatsapp.net")
	if _, err := first.handler.dispatcher.ScheduleTask(context.Background(), key, source, time.Now().Add(time.Hour), "water the plants"); err != nil {
		t.Fatalf("schedule task: %v", err)
	}
	first.stop()
	if err := first.store.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}
	if first.model.calls.Load() != 1 {
		t.Fatalf("task ran before its time: %d model calls", first.model.calls.Load())
	}

	// Move the saved task's time into the past, as if the app was down when
	// it came due; the next start runs it at once.
	second := newFixtureAtPath(t, path, tenantID, accountID, 0, 1)
	tasks, err := second.store.Inbound().ListScheduledTasks(context.Background(), tenantID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("saved tasks = %v, %v", tasks, err)
	}
	if err := second.store.Inbound().DeleteScheduledTask(context.Background(), key, tasks[0].ID); err != nil {
		t.Fatal(err)
	}
	tasks[0].FireAt = time.Now().Add(-time.Minute)
	if _, err := second.store.Inbound().SaveScheduledTask(context.Background(), tasks[0]); err != nil {
		t.Fatal(err)
	}
	if err := second.handler.Recover(context.Background(), tenantID); err != nil {
		t.Fatalf("recover: %v", err)
	}
	second.handler.dispatcher.WaitTasks()
	if err := second.handler.settle(nil); err != nil {
		t.Fatalf("task turn: %v", err)
	}
	if second.model.calls.Load() != 1 || !strings.Contains(second.sender.last().Text, "Task: water the plants") {
		t.Fatalf("model calls = %d, sent %q", second.model.calls.Load(), second.sender.last().Text)
	}
}

// saveDueDailyTask stores a daily task whose run is at fireAt and arms it,
// as a start does.
func saveDueDailyTask(t *testing.T, fixture *fixture, key agent.Key, source identity.MessageID, fireAt time.Time, minute int) inbound.ScheduledTask {
	t.Helper()
	id, _ := identity.NewCausationID()
	invocation, _ := identity.NewInvocationID()
	task := inbound.ScheduledTask{Key: key, ID: id, InvocationID: invocation, Source: source, Prompt: "say good morning", FireAt: fireAt, Daily: true, DailyMinute: minute}
	if _, err := fixture.store.Inbound().SaveScheduledTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if err := fixture.handler.Recover(context.Background(), key.TenantID); err != nil {
		t.Fatalf("recover: %v", err)
	}
	return task
}

// waitRescheduled waits until the daily task has moved to its next run.
func waitRescheduled(t *testing.T, fixture *fixture, task inbound.ScheduledTask) inbound.ScheduledTask {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tasks, err := fixture.store.Inbound().ListScheduledTasks(context.Background(), task.Key.TenantID)
		if err != nil {
			t.Fatal(err)
		}
		if len(tasks) == 1 && tasks[0].InvocationID != task.InvocationID {
			return tasks[0]
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("daily task was not moved to its next run")
	return inbound.ScheduledTask{}
}

func TestDailyTaskRunsAndMovesToTheNextDay(t *testing.T) {
	fixture := newFixture(t)
	key, source := startTaskChat(t, fixture, "15550000035@s.whatsapp.net")
	task := saveDueDailyTask(t, fixture, key, source, time.Now().Add(20*time.Millisecond), 6*60+30)
	next := waitRescheduled(t, fixture, task)
	if err := fixture.handler.settle(nil); err != nil {
		t.Fatalf("task turn: %v", err)
	}
	if fixture.model.calls.Load() != 2 || !strings.Contains(fixture.sender.last().Text, "Daily task (it repeats every day; do not schedule it again) firing now.") {
		t.Fatalf("model calls = %d, sent %q", fixture.model.calls.Load(), fixture.sender.last().Text)
	}
	local := next.FireAt.In(time.Local)
	if !next.Daily || local.Hour() != 6 || local.Minute() != 30 || !next.FireAt.After(time.Now()) || next.FireAt.After(time.Now().Add(24*time.Hour)) {
		t.Fatalf("next run = %v (daily=%v), want the next 06:30 local", local, next.Daily)
	}
}

func TestDailyTaskFarTooLateSkipsThatDay(t *testing.T) {
	fixture := newFixture(t)
	key, source := startTaskChat(t, fixture, "15550000036@s.whatsapp.net")
	task := saveDueDailyTask(t, fixture, key, source, time.Now().Add(-2*time.Hour), 6*60)
	waitRescheduled(t, fixture, task)
	if err := fixture.handler.settle(nil); err != nil {
		t.Fatal(err)
	}
	if fixture.model.calls.Load() != 1 {
		t.Fatalf("a run two hours late was not skipped: %d model calls", fixture.model.calls.Load())
	}
}

func TestCancelTaskDeletesOnlyTheNamedKind(t *testing.T) {
	fixture := newFixture(t)
	key, source := startTaskChat(t, fixture, "15550000037@s.whatsapp.net")
	dispatcher := fixture.handler.dispatcher
	daily, err := dispatcher.ScheduleDailyTask(context.Background(), key, source, 8*60, "post the agenda")
	if err != nil || daily.Code == "" || !daily.Daily {
		t.Fatalf("schedule daily task: %+v, %v", daily, err)
	}
	if listed, err := dispatcher.Tasks(context.Background(), key); err != nil || len(listed) != 1 || listed[0].Code != daily.Code {
		t.Fatalf("tasks = %+v, %v", listed, err)
	}
	if deleted, err := dispatcher.CancelTask(context.Background(), key, daily.Code, false); err != nil || deleted {
		t.Fatalf("one-off cancel of a daily task = %v, %v", deleted, err)
	}
	if deleted, err := dispatcher.CancelTask(context.Background(), key, strings.ToUpper(daily.Code), true); err != nil || !deleted {
		t.Fatalf("daily cancel = %v, %v", deleted, err)
	}
	dispatcher.WaitTasks() // its timer is gone
	if tasks, err := fixture.store.Inbound().ListScheduledTasks(context.Background(), key.TenantID); err != nil || len(tasks) != 0 {
		t.Fatalf("tasks after cancel = %v, %v", tasks, err)
	}
}

func TestDailyTasksAreCappedPerChat(t *testing.T) {
	fixture := newFixture(t)
	key, _ := startTaskChat(t, fixture, "15550000038@s.whatsapp.net")
	dispatcher := fixture.handler.dispatcher
	for index := range inbound.MaxDailyTasks + 1 {
		source, _ := identity.NewMessageID()
		_, err := dispatcher.ScheduleDailyTask(context.Background(), key, source, index, "task")
		if index < inbound.MaxDailyTasks && err != nil {
			t.Fatalf("daily task %d: %v", index, err)
		}
		if index == inbound.MaxDailyTasks && !agent.IsCode(err, agent.ErrorResourceExhausted) {
			t.Fatalf("daily task over the cap: err = %v", err)
		}
	}
}

func TestNextDailyRun(t *testing.T) {
	zone := time.FixedZone("WIB", 7*60*60)
	now := time.Date(2026, 9, 28, 13, 5, 0, 0, time.UTC) // 20:05 local
	for _, test := range []struct {
		minute int
		want   time.Time
	}{
		{7 * 60, time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)},     // 07:00 tomorrow
		{21 * 60, time.Date(2026, 9, 28, 14, 0, 0, 0, time.UTC)},   // 21:00 today
		{20*60 + 5, time.Date(2026, 9, 29, 13, 5, 0, 0, time.UTC)}, // exactly now: tomorrow
	} {
		if got := inbound.NextDailyRun(now, test.minute, zone); !got.Equal(test.want) {
			t.Errorf("minute %d: next run = %v, want %v", test.minute, got, test.want)
		}
	}
}
