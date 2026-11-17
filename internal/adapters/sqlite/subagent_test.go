package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/subagent"
)

func TestSubagentTasksAreOnePerChatAndDurable(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	claimed, err := store.Inbound().ClaimAndResolveSender(ctx, testCandidate(t, "subagent-chat", "15550000077@s.whatsapp.net"))
	if err != nil {
		t.Fatal(err)
	}
	key := agent.Key{TenantID: claimed.Message.TenantID, AccountID: claimed.Message.AccountID, ChatID: claimed.Message.ChatID}
	newTask := func(session string) subagent.Task {
		invocation, _ := identity.NewInvocationID()
		causation, _ := identity.NewCausationID()
		return subagent.Task{Key: key, SessionID: session, Instruction: "convert it", InvocationID: invocation, CausationID: causation,
			FileMessageIDs: []identity.MessageID{claimed.Message.ID}, StartedAt: time.Now()}
	}
	first := newTask("first")
	if err := store.Subagent().CreateTask(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := store.Subagent().CreateTask(ctx, newTask("second")); !agent.IsCode(err, agent.ErrorConflict) {
		t.Fatalf("second running task = %v, want conflict", err)
	}
	active, err := store.Subagent().ActiveTask(ctx, key)
	if err != nil || active == nil || active.SessionID != "first" || len(active.FileMessageIDs) != 1 || active.FileMessageIDs[0] != claimed.Message.ID {
		t.Fatalf("active task = %#v, %v", active, err)
	}
	result := subagent.Result{Success: true, Files: []subagent.OutputFile{{Name: "a.pdf", Path: "/x/a.pdf", Size: 3}}}
	changed, err := store.Subagent().FinishTask(ctx, key.TenantID, "first", subagent.StatusCompleted, "done", result, time.Now())
	if err != nil || !changed {
		t.Fatalf("finish = %v, %v", changed, err)
	}
	if changed, _ := store.Subagent().FinishTask(ctx, key.TenantID, "first", subagent.StatusFailed, "late", subagent.Result{}, time.Now()); changed {
		t.Fatal("a finished task was finished again")
	}
	byTurn, err := store.Subagent().TaskByInvocation(ctx, key, first.InvocationID)
	if err != nil || byTurn == nil || byTurn.Report != "done" || byTurn.Status != subagent.StatusCompleted || len(byTurn.Result.Files) != 1 {
		t.Fatalf("task by invocation = %#v, %v", byTurn, err)
	}
	if err := store.Subagent().MarkDeliveryPartDone(ctx, key.TenantID, "first", "report"); err != nil {
		t.Fatal(err)
	}
	if done, _ := store.Subagent().DeliveryPartDone(ctx, key.TenantID, "first", "report"); !done {
		t.Fatal("delivery part was not recorded")
	}
	undelivered, _ := store.Subagent().ListUndelivered(ctx, key.TenantID)
	if len(undelivered) != 1 {
		t.Fatalf("undelivered = %d, want 1", len(undelivered))
	}
	if err := store.Subagent().MarkTaskDelivered(ctx, key.TenantID, "first", time.Now()); err != nil {
		t.Fatal(err)
	}
	if undelivered, _ := store.Subagent().ListUndelivered(ctx, key.TenantID); len(undelivered) != 0 {
		t.Fatalf("delivered task is still listed: %#v", undelivered)
	}
	// With the first task finished, the chat may run a new one.
	if err := store.Subagent().CreateTask(ctx, newTask("third")); err != nil {
		t.Fatalf("new task after finish = %v", err)
	}
}

func TestSubagentSwitchScopes(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	claimedA, _ := store.Inbound().ClaimAndResolveSender(ctx, testCandidate(t, "switch-a", "15550000078@s.whatsapp.net"))
	keyA := agent.Key{TenantID: claimedA.Message.TenantID, AccountID: claimedA.Message.AccountID, ChatID: claimedA.Message.ChatID}
	if _, set, _ := store.Subagent().SubagentEnabled(ctx, keyA); set {
		t.Fatal("a new chat has a sub-agent setting")
	}
	if err := store.Subagent().SetSubagentEnabled(ctx, keyA, subagent.ScopeDefault, true); err != nil {
		t.Fatal(err)
	}
	if enabled, set, _ := store.Subagent().SubagentEnabled(ctx, keyA); !enabled || !set {
		t.Fatal("the account default did not apply")
	}
	if err := store.Subagent().SetSubagentEnabled(ctx, keyA, subagent.ScopeChat, false); err != nil {
		t.Fatal(err)
	}
	if enabled, _, _ := store.Subagent().SubagentEnabled(ctx, keyA); enabled {
		t.Fatal("the chat setting did not override the default")
	}
	if err := store.Subagent().SetSubagentEnabled(ctx, keyA, subagent.ScopeGlobal, true); err != nil {
		t.Fatal(err)
	}
	if enabled, _, _ := store.Subagent().SubagentEnabled(ctx, keyA); !enabled {
		t.Fatal("global did not clear the chat setting")
	}
}

func TestSubagentMessagesJoinTheTranscriptAndAreQuotable(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	claimed, _ := store.Inbound().ClaimAndResolveSender(ctx, testCandidate(t, "file-chat", "15550000079@s.whatsapp.net"))
	key := agent.Key{TenantID: claimed.Message.TenantID, AccountID: claimed.Message.AccountID, ChatID: claimed.Message.ChatID}
	file := &subagent.SentFile{Path: "/tmp/out.pdf", Name: "out.pdf", Mime: "application/pdf", Kind: "document", Size: 10}
	if err := store.Subagent().RecordSubagentMessage(ctx, key, "【document】 out.pdf", "RECEIPT-1", file, time.Now()); err != nil {
		t.Fatal(err)
	}
	quote, err := resolveQuotedMessage(ctx, store.read, key.TenantID, key.AccountID, key.ChatID, "RECEIPT-1")
	if err != nil || quote == nil || quote.Text != "【document】 out.pdf" {
		t.Fatalf("quote of sent file = %#v, %v", quote, err)
	}
	sent, err := store.Subagent().SentFile(ctx, key, quote.ID)
	if err != nil || sent.Path != file.Path || sent.Kind != "document" {
		t.Fatalf("sent file = %#v, %v", sent, err)
	}
	text, fromBot, err := store.Subagent().MessageText(ctx, key, quote.ID)
	if err != nil || !fromBot || text != "【document】 out.pdf" {
		t.Fatalf("message text = %q %v %v", text, fromBot, err)
	}
}
