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
	if err := dispatcher.ScheduleTask(context.Background(), key, source, fireAt, "remind 【everyone】 about the meeting"); err != nil {
		t.Fatalf("schedule task: %v", err)
	}
	// The same source message schedules one task only.
	if err := dispatcher.ScheduleTask(context.Background(), key, source, fireAt, "remind everyone about the meeting"); err != nil {
		t.Fatalf("schedule duplicate task: %v", err)
	}
	dispatcher.WaitTasks()
	if err := fixture.handler.settle(nil); err != nil {
		t.Fatalf("task turn: %v", err)
	}
	if fixture.model.calls.Load() != 2 || fixture.sender.count() != 2 {
		t.Fatalf("model/sender calls = %d/%d, want 2/2", fixture.model.calls.Load(), fixture.sender.count())
	}
	transcript := fixture.model.lastRequest().Messages[3].Content
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
	if err := dispatcher.ScheduleTask(context.Background(), key, source, time.Now().Add(20*time.Millisecond), "send the wave sticker"); err != nil {
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
	err := fixture.handler.dispatcher.ScheduleTask(context.Background(), key, source, time.Now().Add(25*time.Hour), "too late")
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
	if err := first.handler.dispatcher.ScheduleTask(context.Background(), key, source, time.Now().Add(time.Hour), "water the plants"); err != nil {
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
