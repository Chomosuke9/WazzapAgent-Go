package inbound

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/policy"
)

// MaxTaskDelay is how far ahead a task may be scheduled.
const MaxTaskDelay = 24 * time.Hour

// MaxTaskPromptBytes bounds a task's prompt, leaving room for the framing the
// turn adds around it.
const MaxTaskPromptBytes = 4 * 1024

// MaxDailyTasks is how many daily tasks one chat may have.
const MaxDailyTasks = 10

// maxDailyLateness is how late a daily task may still run, for example after
// the app was down at its time. A later one skips that day.
const maxDailyLateness = time.Hour

// taskCodeLength is how many trailing characters of a task's ID people see
// and type to delete it. The tail of the ID is random.
const taskCodeLength = 6

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
	// Daily is set for a /daily-task. It runs every day at DailyMinute
	// (minutes after local midnight): after each run, FireAt moves to the
	// next day instead of the task being deleted.
	Daily       bool
	DailyMinute int
}

// Code is the short ID shown for the task in /schedule-task list and the
// model's <chat_state>, and typed to delete it.
func (task ScheduledTask) Code() string {
	id := task.ID.String()
	return id[max(len(id)-taskCodeLength, 0):]
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
	if task.Daily && (task.DailyMinute < 0 || task.DailyMinute >= 24*60) {
		return errors.New("daily time must be between 00:00 and 23:59")
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
	// RescheduleScheduledTask moves a daily task to its next run, under a new
	// invocation. It reports false when the task no longer exists.
	RescheduleScheduledTask(ctx context.Context, key agent.Key, id identity.CausationID, invocation identity.InvocationID, fireAt time.Time) (bool, error)
}

// ScheduleTask saves a one-off task and arms its timer. It implements
// command.TaskScheduler for /schedule-task.
func (dispatcher *Dispatcher) ScheduleTask(ctx context.Context, key agent.Key, source identity.MessageID, fireAt time.Time, prompt string) (command.Task, error) {
	// A fire time already past (a command recovered late) runs at once.
	if fireAt.After(dispatcher.options.Clock.Now().Add(MaxTaskDelay + time.Minute)) {
		return command.Task{}, agent.NewError(agent.ErrorInvalidArgument, "schedule task", fmt.Errorf("fire time must be within %s", MaxTaskDelay))
	}
	return dispatcher.saveTask(ctx, ScheduledTask{Key: key, Source: source, Prompt: prompt, FireAt: fireAt.UTC()})
}

// ScheduleDailyTask saves a task that runs every day at minute (minutes
// after midnight, in the bot's time zone) and arms its first run. It
// implements command.TaskScheduler for /daily-task.
func (dispatcher *Dispatcher) ScheduleDailyTask(ctx context.Context, key agent.Key, source identity.MessageID, minute int, prompt string) (command.Task, error) {
	dispatcher.taskEdits.Lock()
	defer dispatcher.taskEdits.Unlock()
	tasks, err := dispatcher.chatTasks(ctx, key)
	if err != nil {
		return command.Task{}, err
	}
	daily := 0
	for _, task := range tasks {
		if task.Daily {
			daily++
		}
	}
	if daily >= MaxDailyTasks {
		return command.Task{}, agent.NewError(agent.ErrorResourceExhausted, "schedule daily task", fmt.Errorf("a chat may have at most %d daily tasks", MaxDailyTasks))
	}
	fireAt := nextDailyRun(dispatcher.options.Clock.Now(), minute, dispatcher.options.Location)
	return dispatcher.saveTask(ctx, ScheduledTask{Key: key, Source: source, Prompt: prompt, FireAt: fireAt, Daily: true, DailyMinute: minute})
}

func (dispatcher *Dispatcher) saveTask(ctx context.Context, task ScheduledTask) (command.Task, error) {
	var err error
	if task.ID, err = identity.NewCausationID(); err != nil {
		return command.Task{}, agent.NewError(agent.ErrorInternal, "schedule task", err)
	}
	if task.InvocationID, err = identity.NewInvocationID(); err != nil {
		return command.Task{}, agent.NewError(agent.ErrorInternal, "schedule task", err)
	}
	if err := task.Validate(); err != nil {
		return command.Task{}, agent.NewError(agent.ErrorInvalidArgument, "schedule task", err)
	}
	created, err := dispatcher.store.SaveScheduledTask(ctx, task)
	if err != nil || !created {
		return command.Task{}, err
	}
	dispatcher.armTask(task)
	return dispatcher.commandTask(task), nil
}

// Tasks lists the chat's tasks, soonest first, with times in the bot's time
// zone. It implements command.TaskScheduler.
func (dispatcher *Dispatcher) Tasks(ctx context.Context, key agent.Key) ([]command.Task, error) {
	tasks, err := dispatcher.chatTasks(ctx, key)
	if err != nil {
		return nil, err
	}
	listed := make([]command.Task, 0, len(tasks))
	for _, task := range tasks {
		listed = append(listed, dispatcher.commandTask(task))
	}
	return listed, nil
}

func (dispatcher *Dispatcher) commandTask(task ScheduledTask) command.Task {
	return command.Task{Code: task.Code(), Prompt: task.Prompt, FireAt: task.FireAt.In(dispatcher.options.Location), Daily: task.Daily}
}

// CancelTask deletes the chat's one-off or daily task with this code and
// stops its timer. It reports false when there is no such task. It
// implements command.TaskScheduler.
func (dispatcher *Dispatcher) CancelTask(ctx context.Context, key agent.Key, code string, daily bool) (bool, error) {
	dispatcher.taskEdits.Lock()
	defer dispatcher.taskEdits.Unlock()
	tasks, err := dispatcher.chatTasks(ctx, key)
	if err != nil {
		return false, err
	}
	for _, task := range tasks {
		if task.Daily != daily || !strings.EqualFold(task.Code(), strings.TrimSpace(code)) {
			continue
		}
		if err := dispatcher.store.DeleteScheduledTask(ctx, key, task.ID); err != nil {
			return false, err
		}
		dispatcher.mu.Lock()
		if timer, armed := dispatcher.tasks[task.ID]; armed {
			// A turn already running finishes; a daily task is then not
			// moved to the next day, because its row is gone.
			timer.Stop()
			delete(dispatcher.tasks, task.ID)
		}
		dispatcher.mu.Unlock()
		return true, nil
	}
	return false, nil
}

func (dispatcher *Dispatcher) chatTasks(ctx context.Context, key agent.Key) ([]ScheduledTask, error) {
	tasks, err := dispatcher.store.ListScheduledTasks(ctx, key.TenantID)
	if err != nil {
		return nil, err
	}
	chat := tasks[:0]
	for _, task := range tasks {
		if task.Key == key {
			chat = append(chat, task)
		}
	}
	return chat, nil
}

// nextDailyRun is the first time after now that the clock in location reads
// minute minutes after midnight.
func nextDailyRun(now time.Time, minute int, location *time.Location) time.Time {
	local := now.In(location)
	next := time.Date(local.Year(), local.Month(), local.Day(), minute/60, minute%60, 0, 0, location)
	if !next.After(now) {
		next = time.Date(local.Year(), local.Month(), local.Day()+1, minute/60, minute%60, 0, 0, location)
	}
	return next.UTC()
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

	var err error
	if !task.Daily || dispatcher.options.Clock.Now().Sub(task.FireAt) <= maxDailyLateness {
		err = dispatcher.runTask(dispatcher.ctx, task)
	}
	if dispatcher.ctx.Err() != nil {
		// Shutdown cut the turn off: keep the task, so the next start runs
		// it again and resumes the same turn.
		return
	}
	if err != nil {
		dispatcher.options.Report(err)
	}
	if task.Daily {
		dispatcher.rescheduleDaily(task)
		return
	}
	// A task runs once, like a command: a failed one is not tried again.
	if deleteErr := dispatcher.store.DeleteScheduledTask(dispatcher.ctx, task.Key, task.ID); deleteErr != nil {
		dispatcher.options.Report(deleteErr)
	}
	dispatcher.mu.Lock()
	delete(dispatcher.tasks, task.ID)
	dispatcher.mu.Unlock()
}

// rescheduleDaily moves a daily task that has run (or failed) to its next
// day and arms it again, unless it was deleted meanwhile.
func (dispatcher *Dispatcher) rescheduleDaily(task ScheduledTask) {
	dispatcher.taskEdits.Lock()
	defer dispatcher.taskEdits.Unlock()
	dispatcher.mu.Lock()
	delete(dispatcher.tasks, task.ID)
	dispatcher.mu.Unlock()
	invocationID, err := identity.NewInvocationID()
	if err != nil {
		// The row keeps its old time; the next start finds it too late to
		// run and moves it on.
		dispatcher.options.Report(agent.NewError(agent.ErrorInternal, "reschedule daily task", err))
		return
	}
	next := task
	next.InvocationID = invocationID
	next.FireAt = nextDailyRun(dispatcher.options.Clock.Now(), task.DailyMinute, dispatcher.options.Location)
	found, err := dispatcher.store.RescheduleScheduledTask(dispatcher.ctx, task.Key, task.ID, next.InvocationID, next.FireAt)
	if err != nil {
		dispatcher.options.Report(err)
		return
	}
	if found {
		dispatcher.armTask(next)
	}
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
		Input:         []agent.ContentPart{agent.TextPart{Text: scheduledTaskText(task)}},
		Capabilities:  capabilities,
		Commands:      commandNames,
		Stickers:      dispatcher.stickerNames(ctx, task.Key),
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
func scheduledTaskText(task ScheduledTask) string {
	kind := "Scheduled task"
	if task.Daily {
		kind = "Daily task (it repeats every day; do not schedule it again)"
	}
	return kind + " firing now. Carry it out in this chat now, in the chat's language, " +
		"without asking for confirmation. To tag someone, use the @Name (senderRef) form. Task: " + task.Prompt
}
