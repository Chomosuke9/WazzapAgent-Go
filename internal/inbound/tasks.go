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
	return dispatcher.runSystemTurn(ctx, task.Key, agent.Invocation{
		ID:          task.InvocationID,
		Cause:       agent.CauseScheduledTask,
		Causation:   agent.CausationRef{Kind: agent.CausationTask, ID: task.ID},
		Input:       []agent.ContentPart{agent.TextPart{Text: scheduledTaskText(task)}},
		RequestedAt: task.FireAt,
	})
}

// runSystemTurn runs a turn that has no sender (a task or a direct request).
// The caller supplies the invocation's identity and text; the chat's config,
// permissions, commands, and stickers are read here.
func (dispatcher *Dispatcher) runSystemTurn(ctx context.Context, key agent.Key, invocation agent.Invocation) error {
	_, err := dispatcher.runSystemTurnWith(ctx, key, invocation, nil)
	return err
}

// runSystemTurnWith is runSystemTurn with a hook: adjust, when set, may
// change the chat context and the invocation just before the model runs.
func (dispatcher *Dispatcher) runSystemTurnWith(ctx context.Context, key agent.Key, invocation agent.Invocation, adjust func(*agent.ChatContext, *agent.Invocation)) (agent.InvokeResult, error) {
	var result agent.InvokeResult
	currentAgent, err := dispatcher.agents.AgentFor(ctx, key)
	if err != nil {
		return result, err
	}
	snapshot, err := currentAgent.Config().Refresh(ctx)
	if err != nil {
		return result, err
	}
	var observedChat *agent.ChatContext
	chatKind := conversation.ChatDirect
	if dispatcher.options.ChatContext != nil {
		if chat, readErr := dispatcher.options.ChatContext.ReadChatContext(ctx, key); readErr == nil {
			observedChat = &chat
			if chat.Kind == "group" {
				chatKind = conversation.ChatGroup
			}
		}
	}
	principal, err := policy.ModelPrincipal(key, invocation.ID)
	if err != nil {
		return result, err
	}
	// The same check as a command the model issues: the kill switch is on
	// and the chat is still allowlisted.
	facts, err := dispatcher.commandPermissionFacts(ctx, principal, snapshot.Permission, true, chatKind)
	if err != nil {
		return result, err
	}
	capabilities, err := dispatcher.policy.ModelCapabilities(snapshot.Permission)
	if err != nil {
		return result, err
	}
	commandNames, err := modelCommandNames(facts)
	if err != nil {
		return result, err
	}
	invocation.Capabilities = capabilities
	invocation.Commands = commandNames
	invocation.Stickers = dispatcher.stickerNames(ctx, key)
	invocation.PolicyVersion = snapshot.Version
	if adjust != nil {
		if observedChat == nil {
			// The hook needs a chat to adjust; without one the turn cannot
			// show the model what the hook adds.
			chat, readErr := currentAgent.ReadChatContext(ctx)
			if readErr != nil {
				return result, readErr
			}
			observedChat = &chat
		}
		adjust(observedChat, &invocation)
	}
	_ = dispatcher.options.Activity.SetComposing(ctx, key, true)
	defer func() {
		pauseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = dispatcher.options.Activity.SetComposing(pauseCtx, key, false)
	}()
	return currentAgent.InvokeWith(ctx, invocation, snapshot, observedChat)
}

// SubagentTurn tells the model a sub-agent task finished: text is the
// 【SUBTASK FINISHED】 system entry at the end of the transcript, and
// resultBlock says, for this turn only, what the model may do with it. The
// images are the task's image files, which the model sees on this turn. The
// turn may call execute_subtask again to correct the result. Running it again
// with the same invocation resumes the same turn. It implements
// subagent.Turns.
func (dispatcher *Dispatcher) SubagentTurn(ctx context.Context, key agent.Key, invocationID identity.InvocationID, causationID identity.CausationID, text, resultBlock string, images []agent.ModelImage, requestedAt time.Time) (agent.InvokeResult, error) {
	if err := key.Validate(); err != nil {
		return agent.InvokeResult{}, err
	}
	text = strings.ToValidUTF8(text, "")
	if len(text) > agent.MaxInputBytes {
		text = strings.ToValidUTF8(text[:agent.MaxInputBytes], "")
	}
	if invocationID.IsZero() || causationID.IsZero() || strings.TrimSpace(text) == "" {
		return agent.InvokeResult{}, agent.NewError(agent.ErrorInvalidArgument, "run sub-agent turn", errors.New("turn identity and text are required"))
	}
	if requestedAt.IsZero() {
		requestedAt = dispatcher.options.Clock.Now()
	}
	dispatcher.mu.Lock()
	if dispatcher.ctx.Err() != nil {
		dispatcher.mu.Unlock()
		return agent.InvokeResult{}, agent.NewError(agent.ErrorNotReady, "run sub-agent turn", errors.New("the application is shutting down"))
	}
	dispatcher.turns.Add(1)
	dispatcher.mu.Unlock()
	defer dispatcher.turns.Done()
	// Nobody is waiting to ask again, so the model call is retried through
	// rate limits and outages like a direct invoke's.
	if dispatcher.options.PersistModel != nil {
		defer dispatcher.options.PersistModel(invocationID)()
	}
	turnCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(dispatcher.ctx, cancel)
	defer stop()
	return dispatcher.runSystemTurnWith(turnCtx, key, agent.Invocation{
		ID:          invocationID,
		Cause:       agent.CauseSubagentResult,
		Causation:   agent.CausationRef{Kind: agent.CausationSubagent, ID: causationID},
		Input:       []agent.ContentPart{agent.TextPart{Text: text}},
		RequestedAt: requestedAt,
	}, func(chat *agent.ChatContext, invocation *agent.Invocation) {
		// The task was started while the chat's sub-agent was on, so its
		// result may be corrected even if the switch was turned off since.
		chat.Subagent = &agent.SubagentContext{Result: resultBlock, ResultImages: images}
		invocation.Capabilities = invocation.Capabilities.With(agent.CapabilitySubagent)
	})
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

// DirectInvoke runs prompt as a turn of its own in the chat, now, and
// returns once it is accepted: the turn runs in the background and its
// failure is reported through Options.Report. Like a task it has no sender;
// the model sees a system entry. A failing model call is retried, if the
// model layer was given PersistModel, instead of the request being lost.
func (dispatcher *Dispatcher) DirectInvoke(ctx context.Context, key agent.Key, prompt string) error {
	if err := key.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(prompt) == "" || !utf8.ValidString(prompt) || len(prompt) > MaxTaskPromptBytes {
		return agent.NewError(agent.ErrorInvalidArgument, "direct invoke", fmt.Errorf("prompt must be non-empty valid UTF-8 within %d bytes", MaxTaskPromptBytes))
	}
	invocationID, err := identity.NewInvocationID()
	if err != nil {
		return agent.NewError(agent.ErrorInternal, "direct invoke", err)
	}
	causationID, err := identity.NewCausationID()
	if err != nil {
		return agent.NewError(agent.ErrorInternal, "direct invoke", err)
	}
	dispatcher.mu.Lock()
	if dispatcher.ctx.Err() != nil {
		dispatcher.mu.Unlock()
		return agent.NewError(agent.ErrorNotReady, "direct invoke", errors.New("the application is shutting down"))
	}
	dispatcher.turns.Add(1)
	dispatcher.mu.Unlock()

	release := func() {}
	if dispatcher.options.PersistModel != nil {
		release = dispatcher.options.PersistModel(invocationID)
	}
	invocation := agent.Invocation{
		ID:          invocationID,
		Cause:       agent.CauseDirectRequest,
		Causation:   agent.CausationRef{Kind: agent.CausationRequest, ID: causationID},
		Input:       []agent.ContentPart{agent.TextPart{Text: directInvokeText(prompt)}},
		RequestedAt: dispatcher.options.Clock.Now(),
	}
	go func() {
		defer dispatcher.turns.Done()
		defer release()
		if err := dispatcher.runSystemTurn(dispatcher.ctx, key, invocation); err != nil && dispatcher.ctx.Err() == nil {
			dispatcher.options.Report(err)
		}
	}()
	return nil
}

// directInvokeText is what the model reads for a direct invoke.
func directInvokeText(prompt string) string {
	return "Direct request firing now. Carry it out in this chat now, in the chat's language, " +
		"without asking for confirmation. To tag someone, use the @Name (senderRef) form. Request: " + prompt
}
