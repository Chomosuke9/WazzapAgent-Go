package inbound

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/policy"
)

// MaxTaskDelay is how far ahead a task may be scheduled.
const MaxTaskDelay = 24 * time.Hour

// MaxTaskPromptBytes bounds a task's prompt, leaving room for the framing the
// turn adds around it.
const MaxTaskPromptBytes = 4 * 1024

// ScheduledTask is a prompt the bot runs as an AI turn in one chat at FireAt.
type ScheduledTask struct {
	Key agent.Key
	// ID names the task and is the causation of the turn it runs.
	ID identity.CausationID
	// InvocationID is fixed when the task is saved, so running the task again
	// after a crash resumes the same turn instead of starting a second one.
	InvocationID identity.InvocationID
	// Source is the command message that scheduled the task.
	Source identity.MessageID
	Prompt string
	FireAt time.Time
}

func (task ScheduledTask) Validate() error {
	if err := task.Key.Validate(); err != nil {
		return err
	}
	if task.ID.IsZero() || task.InvocationID.IsZero() || task.Source.IsZero() || task.FireAt.IsZero() {
		return errors.New("task, invocation, and source IDs and a fire time are required")
	}
	if strings.TrimSpace(task.Prompt) == "" || !utf8.ValidString(task.Prompt) || len(task.Prompt) > MaxTaskPromptBytes {
		return fmt.Errorf("prompt must be non-empty valid UTF-8 within %d bytes", MaxTaskPromptBytes)
	}
	return nil
}

// TaskStore keeps scheduled tasks until they have run.
type TaskStore interface {
	// SaveScheduledTask reports whether task is new; a task from the same
	// source message is stored only once.
	SaveScheduledTask(context.Context, ScheduledTask) (bool, error)
	ListScheduledTasks(context.Context, identity.TenantID) ([]ScheduledTask, error)
	DeleteScheduledTask(context.Context, agent.Key, identity.CausationID) error
}

// ScheduleTask saves a task and arms its timer. It implements
// command.TaskScheduler for /schedule-task.
func (dispatcher *Dispatcher) ScheduleTask(ctx context.Context, key agent.Key, source identity.MessageID, fireAt time.Time, prompt string) error {
	// A fire time already past (a command recovered late) runs at once.
	if fireAt.After(dispatcher.options.Clock.Now().Add(MaxTaskDelay + time.Minute)) {
		return agent.NewError(agent.ErrorInvalidArgument, "schedule task", fmt.Errorf("fire time must be within %s", MaxTaskDelay))
	}
	id, err := identity.NewCausationID()
	if err != nil {
		return agent.NewError(agent.ErrorInternal, "schedule task", err)
	}
	invocationID, err := identity.NewInvocationID()
	if err != nil {
		return agent.NewError(agent.ErrorInternal, "schedule task", err)
	}
	task := ScheduledTask{Key: key, ID: id, InvocationID: invocationID, Source: source, Prompt: prompt, FireAt: fireAt.UTC()}
	if err := task.Validate(); err != nil {
		return agent.NewError(agent.ErrorInvalidArgument, "schedule task", err)
	}
	created, err := dispatcher.store.SaveScheduledTask(ctx, task)
	if err != nil || !created {
		return err
	}
	dispatcher.armTask(task)
	return nil
}

// armStoredTasks arms every saved task that has no timer yet. A task that
// came due while the app was down runs at once.
func (dispatcher *Dispatcher) armStoredTasks(ctx context.Context, tenantID identity.TenantID) error {
	tasks, err := dispatcher.store.ListScheduledTasks(ctx, tenantID)
	if err != nil {
		return err
	}
	for _, task := range tasks {
		dispatcher.armTask(task)
	}
	return nil
}

func (dispatcher *Dispatcher) armTask(task ScheduledTask) {
	dispatcher.mu.Lock()
	defer dispatcher.mu.Unlock()
	if dispatcher.ctx.Err() != nil {
		return // the next start arms it from the store
	}
	if _, armed := dispatcher.tasks[task.ID]; armed {
		return
	}
	delay := max(task.FireAt.Sub(dispatcher.options.Clock.Now()), 0)
	dispatcher.tasks[task.ID] = time.AfterFunc(delay, func() { dispatcher.fireTask(task) })
}

func (dispatcher *Dispatcher) fireTask(task ScheduledTask) {
	dispatcher.mu.Lock()
	if dispatcher.ctx.Err() != nil {
		dispatcher.mu.Unlock()
		return
	}
	dispatcher.turns.Add(1)
	dispatcher.mu.Unlock()
	defer dispatcher.turns.Done()

	err := dispatcher.runTask(dispatcher.ctx, task)
	if dispatcher.ctx.Err() != nil {
		// Shutdown cut the turn off: keep the task, so the next start runs
		// it again and resumes the same turn.
		return
	}
	if err != nil {
		dispatcher.options.Report(err)
	}
	// A task runs once, like a command: a failed one is not tried again.
	if deleteErr := dispatcher.store.DeleteScheduledTask(dispatcher.ctx, task.Key, task.ID); deleteErr != nil {
		dispatcher.options.Report(deleteErr)
	}
	dispatcher.mu.Lock()
	delete(dispatcher.tasks, task.ID)
	dispatcher.mu.Unlock()
}

// runTask answers task as a turn of its own. The task has no sender: the
// model sees it as a system entry at the end of the chat transcript.
func (dispatcher *Dispatcher) runTask(ctx context.Context, task ScheduledTask) error {
	currentAgent, err := dispatcher.agents.AgentFor(ctx, task.Key)
	if err != nil {
		return err
	}
	snapshot, err := currentAgent.Config().Refresh(ctx)
	if err != nil {
		return err
	}
	var observedChat *agent.ChatContext
	chatKind := conversation.ChatDirect
	if dispatcher.options.ChatContext != nil {
		if chat, readErr := dispatcher.options.ChatContext.ReadChatContext(ctx, task.Key); readErr == nil {
			observedChat = &chat
			if chat.Kind == "group" {
				chatKind = conversation.ChatGroup
			}
		}
	}
	principal, err := policy.ModelPrincipal(task.Key, task.InvocationID)
	if err != nil {
		return err
	}
	// The same check as a command the model issues: the kill switch is on
	// and the chat is still allowlisted.
	facts, err := dispatcher.commandPermissionFacts(ctx, principal, snapshot.Permission, true, chatKind)
	if err != nil {
		return err
	}
	capabilities, err := dispatcher.policy.ModelCapabilities(snapshot.Permission)
	if err != nil {
		return err
	}
	commandNames, err := modelCommandNames(facts)
	if err != nil {
		return err
	}
	invocation := agent.Invocation{
		ID:            task.InvocationID,
		Cause:         agent.CauseScheduledTask,
		Causation:     agent.CausationRef{Kind: agent.CausationTask, ID: task.ID},
		Input:         []agent.ContentPart{agent.TextPart{Text: scheduledTaskText(task.Prompt)}},
		Capabilities:  capabilities,
		Commands:      commandNames,
		PolicyVersion: snapshot.Version,
		RequestedAt:   task.FireAt,
	}
	_ = dispatcher.options.Activity.SetComposing(ctx, task.Key, true)
	defer func() {
		pauseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = dispatcher.options.Activity.SetComposing(pauseCtx, task.Key, false)
	}()
	_, err = currentAgent.InvokeWith(ctx, invocation, snapshot, observedChat)
	return err
}

// scheduledTaskText is what the model reads when a task fires.
func scheduledTaskText(prompt string) string {
	return "Scheduled task firing now. Carry it out in this chat now, in the chat's language, " +
		"without asking for confirmation. To tag someone, use the @Name (senderRef) form. Task: " + prompt
}
