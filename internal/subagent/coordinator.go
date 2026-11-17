package subagent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/effect"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

const (
	// deliveryAttempts is how often one delivery is tried before it is left
	// for the next Recover.
	deliveryAttempts = 4
	// turnAttempts is how often the delivering turn is generated when the
	// model call fails in a way that can pass.
	turnAttempts = 3
	// queueDedupWindow suppresses a queue notice repeated by a callback retry.
	queueDedupWindow = 5 * time.Second
	// outputRetention is how long received output files are kept, so a later
	// task can take a file the bot sent as input.
	outputRetention = 14 * 24 * time.Hour
)

// turnRetryDelay is the wait between attempts of a result turn. A variable
// only so tests can shorten it.
var turnRetryDelay = 2 * time.Second

// Coordinator runs the chat side of sub-agent tasks: it starts or steers a
// task for execute_subtask, waits for the service's callback, and delivers
// the result. One Coordinator serves one WhatsApp account.
type Coordinator struct {
	config    Config
	tenantID  identity.TenantID
	accountID identity.AccountID
	store     Store
	client    *Client
	messenger Messenger
	inputs    Inputs
	clock     agent.Clock
	logger    *slog.Logger

	ctx    context.Context // outlives calls; cancelled by Close
	cancel context.CancelFunc
	work   sync.WaitGroup
	// authorize, when set, must allow the bot to send to a chat before a
	// task starts there.
	authorize func(context.Context, agent.Key) error

	mu     sync.Mutex
	closed bool
	turns  Turns
	// live holds what a running task keeps only in memory.
	live map[string]*liveTask
	// owned names the sessions a goroutine here is waiting for or delivering.
	owned map[string]bool
	// corrected names the finished sessions whose delivering turn started a
	// correction task.
	corrected map[string]bool
	// steering serializes the steering of one task, in arrival order.
	steering  map[string]*sync.Mutex
	queueSent map[string]queueNotice
}

// liveTask is the in-memory side of a running task.
type liveTask struct {
	progress []ProgressEntry
	// activity is signalled by every callback that shows the task is alive.
	activity chan struct{}
	// done is closed when the task finished.
	done     chan struct{}
	doneOnce sync.Once
}

type queueNotice struct {
	position, size int64
	at             time.Time
}

type Dependencies struct {
	TenantID  identity.TenantID
	AccountID identity.AccountID
	Store     Store
	Messenger Messenger
	Inputs    Inputs
	Clock     agent.Clock
	Logger    *slog.Logger
	// Authorize, when set, is the send policy (the Agent switch and the
	// allowlist) checked before a task starts or is steered.
	Authorize func(context.Context, agent.Key) error
}

func NewCoordinator(config Config, dependencies Dependencies) (*Coordinator, error) {
	config = config.withDefaults()
	config.URL = strings.TrimRight(strings.TrimSpace(config.URL), "/")
	if config.URL == "" || strings.TrimSpace(config.WebhookURL) == "" || strings.TrimSpace(config.DataDir) == "" {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "create sub-agent coordinator", errors.New("service URL, webhook URL and data directory are required"))
	}
	if dependencies.TenantID.IsZero() || dependencies.AccountID.IsZero() || dependencies.Store == nil ||
		dependencies.Messenger == nil || dependencies.Inputs == nil {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "create sub-agent coordinator", errors.New("account, store, messenger and inputs are required"))
	}
	if dependencies.Clock == nil {
		dependencies.Clock = agent.SystemClock{}
	}
	if dependencies.Logger == nil {
		dependencies.Logger = slog.New(slog.DiscardHandler)
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Coordinator{
		config: config, tenantID: dependencies.TenantID, accountID: dependencies.AccountID,
		store: dependencies.Store, client: NewClient(config), messenger: dependencies.Messenger,
		inputs: dependencies.Inputs, clock: dependencies.Clock, logger: dependencies.Logger,
		ctx: ctx, cancel: cancel, authorize: dependencies.Authorize,
		live: make(map[string]*liveTask), owned: make(map[string]bool), corrected: make(map[string]bool),
		steering: make(map[string]*sync.Mutex), queueSent: make(map[string]queueNotice),
	}, nil
}

// BindTurns sets what runs the delivering turn. It is separate from
// NewCoordinator because the turn runner itself needs the Coordinator.
func (coordinator *Coordinator) BindTurns(turns Turns) {
	coordinator.mu.Lock()
	coordinator.turns = turns
	coordinator.mu.Unlock()
}

// Close stops waiting for tasks and waits for running deliveries to return.
// Tasks stay saved: the next start picks them up again.
func (coordinator *Coordinator) Close() {
	coordinator.mu.Lock()
	coordinator.closed = true
	coordinator.mu.Unlock()
	coordinator.cancel()
	coordinator.work.Wait()
}

// spawn runs fn in the background unless the Coordinator is closed.
func (coordinator *Coordinator) spawn(fn func()) bool {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.closed {
		return false
	}
	coordinator.work.Go(fn)
	return true
}

// Enabled reports whether the chat's model gets execute_subtask.
func (coordinator *Coordinator) Enabled(ctx context.Context, key agent.Key) bool {
	enabled, set, err := coordinator.store.SubagentEnabled(ctx, key)
	if err != nil {
		return false
	}
	if !set {
		return coordinator.config.EnabledDefault
	}
	return enabled
}

// SetEnabled saves /subagent on|off for scope.
func (coordinator *Coordinator) SetEnabled(ctx context.Context, key agent.Key, scope Scope, enabled bool) error {
	return coordinator.store.SetSubagentEnabled(ctx, key, scope, enabled)
}

// ChatContext is the chat's <subagent> block.
func (coordinator *Coordinator) ChatContext(ctx context.Context, key agent.Key) *agent.SubagentContext {
	if !coordinator.Enabled(ctx, key) {
		return &agent.SubagentContext{Off: true}
	}
	return &agent.SubagentContext{Status: coordinator.statusBlock(ctx, key)}
}

// ExecuteSubtaskEffect handles one execute_subtask call. A chat with a
// running task gets the instruction forwarded to that task as steering;
// otherwise a new task starts. Either way it returns at once: files are
// fetched and the service is called in the background, so the chat is free
// while the sub-agent works.
func (coordinator *Coordinator) ExecuteSubtaskEffect(ctx context.Context, stored effect.Stored, value effect.Subtask) (string, error) {
	key := stored.Request.Ref.Key
	if key.TenantID != coordinator.tenantID || key.AccountID != coordinator.accountID {
		return "", agent.NewError(agent.ErrorPermissionDenied, "run sub-agent task", errors.New("chat belongs to another account"))
	}
	instruction := strings.TrimSpace(value.Instruction)
	if instruction == "" {
		return "", agent.NewError(agent.ErrorInvalidArgument, "run sub-agent task", errors.New("instruction is empty"))
	}
	// The turn that delivers a result may always correct it, even if the
	// chat's switch was turned off while the task ran.
	previous, err := coordinator.store.TaskByInvocation(ctx, key, stored.Request.InvocationID)
	if err != nil {
		return "", err
	}
	if previous == nil && !coordinator.Enabled(ctx, key) {
		return "", agent.NewError(agent.ErrorPermissionDenied, "run sub-agent task", errors.New("the sub-agent is off for this chat"))
	}
	if coordinator.authorize != nil {
		if err := coordinator.authorize(ctx, key); err != nil {
			return "", err
		}
	}
	for range 2 {
		active, err := coordinator.store.ActiveTask(ctx, key)
		if err != nil {
			return "", err
		}
		if active != nil {
			coordinator.steer(*active, instruction, value.FileMessageIDs)
			return "steer:" + active.SessionID, nil
		}
		task, err := coordinator.newTask(key, instruction, value)
		if err != nil {
			return "", err
		}
		if previous != nil {
			task.PreviousSessionID = previous.SessionID
		}
		err = coordinator.store.CreateTask(ctx, task)
		if agent.IsCode(err, agent.ErrorConflict) {
			continue // another call started a task just now: steer it
		}
		if err != nil {
			return "", err
		}
		coordinator.mu.Lock()
		if previous != nil {
			coordinator.corrected[previous.SessionID] = true
		}
		coordinator.owned[task.SessionID] = true
		coordinator.mu.Unlock()
		coordinator.liveFor(task.SessionID)
		coordinator.logger.Info("sub-agent task started", "session", task.SessionID, "files", len(task.FileMessageIDs),
			"high_quality", task.HighQuality, "correction", previous != nil)
		if !coordinator.spawn(func() { coordinator.run(task) }) {
			coordinator.release(task.SessionID)
			// The task is saved; the next start submits nothing and gives it up.
			return "", agent.NewError(agent.ErrorNotReady, "run sub-agent task", errors.New("the application is shutting down"))
		}
		return "task:" + task.SessionID, nil
	}
	return "", agent.NewError(agent.ErrorConflict, "run sub-agent task", errors.New("the chat's sub-agent task changed while starting"))
}

func (coordinator *Coordinator) newTask(key agent.Key, instruction string, value effect.Subtask) (Task, error) {
	suffix, suffixErr := randomHex(4)
	invocationID, invocationErr := identity.NewInvocationID()
	causationID, causationErr := identity.NewCausationID()
	if err := errors.Join(suffixErr, invocationErr, causationErr); err != nil {
		return Task{}, agent.NewError(agent.ErrorInternal, "create sub-agent task", err)
	}
	now := coordinator.clock.Now()
	return Task{
		Key: key, SessionID: fmt.Sprintf("%s_%s_%d", key.ChatID.String(), suffix, now.Unix()),
		Instruction: instruction, HighQuality: value.HighQuality,
		FileMessageIDs: append([]identity.MessageID(nil), value.FileMessageIDs...),
		InvocationID:   invocationID, CausationID: causationID, Status: StatusRunning, StartedAt: now,
	}, nil
}

// steer forwards instruction and files to a running task, in the background.
// Steering of one task is serialized so instructions arrive in order.
func (coordinator *Coordinator) steer(task Task, instruction string, fileMessageIDs []identity.MessageID) {
	coordinator.mu.Lock()
	lock := coordinator.steering[task.SessionID]
	if lock == nil {
		lock = &sync.Mutex{}
		coordinator.steering[task.SessionID] = lock
	}
	coordinator.mu.Unlock()
	replyTo := lastMessage(fileMessageIDs)
	coordinator.logger.Info("sub-agent steering", "session", task.SessionID, "files", len(fileMessageIDs))
	coordinator.spawn(func() {
		lock.Lock()
		defer lock.Unlock()
		ctx := coordinator.ctx
		suffix, err := randomHex(4)
		if err == nil {
			dir := filepath.Join(coordinator.inputDir(task.SessionID), "steer-"+suffix)
			defer os.RemoveAll(dir)
			var files []string
			if files, err = coordinator.resolveInputs(ctx, task.Key, dir, fileMessageIDs); err == nil {
				err = coordinator.client.Steer(ctx, task.SessionID, instruction, files)
			}
		}
		if err != nil && ctx.Err() == nil {
			coordinator.logger.Error("sub-agent steering failed", "session", task.SessionID, "error", err)
			coordinator.notify(task.Key, "I couldn't deliver that update and its file to the running task. "+
				"Please resend the file or try the instruction again.", replyTo)
		}
	})
}

// run takes one new task from its files to its delivery.
func (coordinator *Coordinator) run(task Task) {
	defer coordinator.release(task.SessionID)
	ctx := coordinator.ctx
	dir := coordinator.inputDir(task.SessionID)
	defer os.RemoveAll(dir)
	files, err := coordinator.resolveInputs(ctx, task.Key, dir, task.FileMessageIDs)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		// Nothing was started, so there is no result to deliver: the chat is
		// told and the task is closed.
		coordinator.logger.Error("sub-agent input files failed", "session", task.SessionID, "error", err)
		coordinator.notify(task.Key, "I couldn't retrieve and deliver the requested file to the task, so I "+
			"didn't start it. Please resend the file and try again.", lastMessage(task.FileMessageIDs))
		coordinator.finish(task.SessionID, Result{}, "The task was not started: its input files could not be retrieved.")
		coordinator.record(func(ctx context.Context) error {
			return coordinator.store.MarkTaskDelivered(ctx, coordinator.tenantID, task.SessionID, coordinator.clock.Now())
		})
		return
	}
	err = coordinator.client.Submit(ctx, task.SessionID, task.Instruction, files, task.HighQuality, task.PreviousSessionID)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		// No callback will come, so the failure is the result.
		coordinator.logger.Error("sub-agent submit failed", "session", task.SessionID, "error", err)
		coordinator.finish(task.SessionID, Result{}, "Failed to submit task to sub-agent: "+err.Error())
	} else {
		coordinator.record(func(ctx context.Context) error {
			return coordinator.store.MarkTaskSubmitted(ctx, coordinator.tenantID, task.SessionID)
		})
	}
	if coordinator.wait(task) {
		coordinator.deliver(task.SessionID)
	}
}

// wait blocks until the task finished, giving it up when it has been silent
// for WaitTimeout or running for MaxWait. It reports false when the app is
// shutting down instead.
func (coordinator *Coordinator) wait(task Task) bool {
	live := coordinator.liveFor(task.SessionID)
	total := time.NewTimer(max(0, task.StartedAt.Add(coordinator.config.MaxWait).Sub(coordinator.clock.Now())))
	defer total.Stop()
	silent := time.NewTimer(coordinator.config.WaitTimeout)
	defer silent.Stop()
	for {
		select {
		case <-live.done:
			return true
		case <-coordinator.ctx.Done():
			return false
		case <-live.activity:
			// The task is alive: a slow task is not a dead one.
			silent.Reset(coordinator.config.WaitTimeout)
		case <-silent.C:
			coordinator.logger.Error("sub-agent task went silent", "session", task.SessionID)
			coordinator.finish(task.SessionID, Result{}, fmt.Sprintf(
				"Sub-agent did not return a result within %ds and sent no progress updates. "+
					"This likely means the sub-agent service crashed or the network is partitioned.",
				int(coordinator.config.WaitTimeout.Seconds())))
			return true
		case <-total.C:
			coordinator.logger.Error("sub-agent task exceeded its maximum run time", "session", task.SessionID)
			coordinator.finish(task.SessionID, Result{}, fmt.Sprintf(
				"Sub-agent did not return a result within %ds total. The task may be too complex or the sub-agent is stuck.",
				int(coordinator.config.MaxWait.Seconds())))
			return true
		}
	}
}

// finish ends a running task with result and report and wakes its waiter. A
// task that already ended keeps its first outcome.
func (coordinator *Coordinator) finish(sessionID string, result Result, report string) (bool, error) {
	status := StatusFailed
	if result.Success {
		status = StatusCompleted
	}
	report = truncate(strings.ToValidUTF8(report, ""), coordinator.config.ReportMaxChars)
	var changed bool
	err := coordinator.record(func(ctx context.Context) (err error) {
		changed, err = coordinator.store.FinishTask(ctx, coordinator.tenantID, sessionID, status, report, result, coordinator.clock.Now())
		return err
	})
	if err != nil {
		coordinator.logger.Error("sub-agent task could not be finished", "session", sessionID, "error", err)
		return false, err
	}
	coordinator.mu.Lock()
	live := coordinator.live[sessionID]
	delete(coordinator.queueSent, sessionID)
	delete(coordinator.steering, sessionID)
	coordinator.mu.Unlock()
	if live != nil {
		live.doneOnce.Do(func() { close(live.done) })
	}
	return changed, nil
}

// record writes with a context of its own, so an outcome is saved even
// while the app shuts down.
func (coordinator *Coordinator) record(write func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return write(ctx)
}

func (coordinator *Coordinator) liveFor(sessionID string) *liveTask {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	live := coordinator.live[sessionID]
	if live == nil {
		live = &liveTask{activity: make(chan struct{}, 1), done: make(chan struct{})}
		coordinator.live[sessionID] = live
	}
	return live
}

// release ends a goroutine's hold on a session.
func (coordinator *Coordinator) release(sessionID string) {
	coordinator.mu.Lock()
	delete(coordinator.owned, sessionID)
	delete(coordinator.live, sessionID)
	coordinator.mu.Unlock()
}

// claim takes a session for one goroutine. It reports false when another
// already holds it.
func (coordinator *Coordinator) claim(sessionID string) bool {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.owned[sessionID] || coordinator.ctx.Err() != nil {
		return false
	}
	coordinator.owned[sessionID] = true
	return true
}

// Recover picks up the tasks no goroutine here holds: after a restart, and
// after a delivery that failed (for example while WhatsApp was offline). A
// running task is waited for again, a finished one is delivered. Call it
// once the account is connected.
func (coordinator *Coordinator) Recover(ctx context.Context) error {
	tasks, err := coordinator.store.ListUndelivered(ctx, coordinator.tenantID)
	if err != nil {
		return err
	}
	for _, task := range tasks {
		if task.Key.AccountID != coordinator.accountID || !coordinator.claim(task.SessionID) {
			continue
		}
		if !coordinator.spawn(func() {
			defer coordinator.release(task.SessionID)
			if task.Status == StatusRunning {
				if !task.Submitted {
					// The app stopped before the service had the task.
					coordinator.finish(task.SessionID, Result{}, "The task was interrupted by a restart before it reached the sub-agent.")
				} else if !coordinator.wait(task) {
					return
				}
			}
			coordinator.deliver(task.SessionID)
		}) {
			coordinator.release(task.SessionID)
		}
	}
	coordinator.pruneOutputs()
	return nil
}

// deliver tells the chat a finished task's result, trying again a few times.
// A delivery that still fails stays saved for the next Recover.
func (coordinator *Coordinator) deliver(sessionID string) {
	var err error
	for attempt := range deliveryAttempts {
		if attempt > 0 {
			if sleepContext(coordinator.ctx, time.Second<<(attempt-1)) != nil {
				return
			}
		}
		if err = coordinator.deliverOnce(coordinator.ctx, sessionID); err == nil || coordinator.ctx.Err() != nil {
			return
		}
		if permanentDeliveryError(err) {
			// The chat can no longer be sent to: trying again would fail the
			// same way every minute.
			coordinator.logger.Error("sub-agent result cannot be delivered; giving up", "session", sessionID, "code", agent.CodeOf(err), "error", err)
			coordinator.record(func(ctx context.Context) error {
				return coordinator.store.MarkTaskDelivered(ctx, coordinator.tenantID, sessionID, coordinator.clock.Now())
			})
			return
		}
	}
	coordinator.logger.Error("sub-agent result delivery failed; it will be tried again", "session", sessionID, "error", err)
}

func permanentDeliveryError(err error) bool {
	switch agent.CodeOf(err) {
	case agent.ErrorPermissionDenied, agent.ErrorNotFound, agent.ErrorInvalidArgument, agent.ErrorIntegrityFailure:
		return true
	}
	return false
}

// deliverOnce runs the model on the finished task so it can summarize the
// report (or start a correction), then sends each output file as a message
// of its own. Every part is recorded once sent, so a retry sends only what
// is missing. If the model produced no reply, the raw report is sent: the
// chat must never be left with only "on it".
func (coordinator *Coordinator) deliverOnce(ctx context.Context, sessionID string) error {
	task, err := coordinator.store.LoadTask(ctx, coordinator.tenantID, sessionID)
	if err != nil {
		return err
	}
	if !task.DeliveredAt.IsZero() || task.Status == StatusRunning {
		return nil
	}
	completed := task.Status == StatusCompleted
	var staged []StagedFile
	var skipped []SkippedFile
	if completed {
		staged, skipped = stageOutputs(task.Result.Files)
		if len(skipped) > 0 {
			coordinator.logger.Warn("sub-agent output files skipped", "session", sessionID, "count", len(skipped))
		}
	}
	finished := finishedLines(task.Report, completed, formatFileList(staged, skipped), task.Result.ContentDropped, len(staged) > 0)

	replied, turnErr := coordinator.resultTurn(ctx, task, finished, staged)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	coordinator.mu.Lock()
	corrected := coordinator.corrected[sessionID]
	coordinator.mu.Unlock()
	if turnErr != nil {
		coordinator.logger.Error("sub-agent result turn failed; sending the raw report", "session", sessionID, "error", turnErr)
	}
	if !replied && !corrected {
		report := task.Report
		if report == "" && completed {
			report = "(Sub-agent finished but produced no report.)"
		} else if report == "" {
			report = "Sub-agent failed without a report."
		}
		if err := coordinator.sendPart(ctx, sessionID, "report", func() error {
			return coordinator.messenger.SendText(ctx, task.Key, report, identity.MessageID{})
		}); err != nil {
			return err
		}
	}
	for index, file := range staged {
		part := fmt.Sprintf("file:%d:%s:%d", index, file.Name, file.Size)
		if err := coordinator.sendPart(ctx, sessionID, part, func() error {
			return coordinator.messenger.SendFile(ctx, task.Key, file)
		}); err != nil {
			return err
		}
	}
	if err := coordinator.store.MarkTaskDelivered(ctx, coordinator.tenantID, sessionID, coordinator.clock.Now()); err != nil {
		return err
	}
	coordinator.mu.Lock()
	delete(coordinator.corrected, sessionID)
	coordinator.mu.Unlock()
	coordinator.logger.Info("sub-agent result delivered", "session", sessionID, "success", completed, "files", len(staged), "correction", corrected)
	return nil
}

// sendPart sends one part of a result unless it was sent before.
func (coordinator *Coordinator) sendPart(ctx context.Context, sessionID, part string, send func() error) error {
	done, err := coordinator.store.DeliveryPartDone(ctx, coordinator.tenantID, sessionID, part)
	if err != nil || done {
		return err
	}
	if err := send(); err != nil {
		return err
	}
	return coordinator.record(func(ctx context.Context) error {
		return coordinator.store.MarkDeliveryPartDone(ctx, coordinator.tenantID, sessionID, part)
	})
}

// resultTurn runs the model on the finished task, showing it the task's
// image files. It reports whether the
// model replied with text. The turn is the task's own invocation, so running
// it again after a failure resumes it instead of answering twice.
func (coordinator *Coordinator) resultTurn(ctx context.Context, task Task, finished string, staged []StagedFile) (bool, error) {
	done, err := coordinator.store.DeliveryPartDone(ctx, coordinator.tenantID, task.SessionID, "turn")
	if err != nil {
		return false, err
	}
	if done {
		// An earlier attempt already ran the turn and only a later part failed.
		replied, err := coordinator.store.DeliveryPartDone(ctx, coordinator.tenantID, task.SessionID, "turn:replied")
		return replied, err
	}
	coordinator.mu.Lock()
	turns := coordinator.turns
	coordinator.mu.Unlock()
	if turns == nil {
		return false, agent.NewError(agent.ErrorNotReady, "deliver sub-agent result", errors.New("no turn runner is bound"))
	}
	images := resultImages(staged)
	var result agent.InvokeResult
	for attempt := range turnAttempts {
		if attempt > 0 {
			if err := sleepContext(ctx, turnRetryDelay); err != nil {
				return false, err
			}
		}
		result, err = turns.SubagentTurn(ctx, task.Key, task.InvocationID, task.CausationID, finished, resultBlock(finished), images, task.EndedAt)
		if err == nil || !agent.RetryableGeneration(err) || ctx.Err() != nil {
			break
		}
	}
	// A reply that is planned is in the outbox and will be sent even if it
	// could not go out just now.
	replied := strings.TrimSpace(result.Text) != "" && !result.ActionID.IsZero()
	if err != nil && !replied && agent.IsCode(err, agent.ErrorNotReady) {
		return false, err // WhatsApp is offline: try the whole delivery later
	}
	if recordErr := coordinator.record(func(ctx context.Context) error {
		if replied {
			if err := coordinator.store.MarkDeliveryPartDone(ctx, coordinator.tenantID, task.SessionID, "turn:replied"); err != nil {
				return err
			}
		}
		return coordinator.store.MarkDeliveryPartDone(ctx, coordinator.tenantID, task.SessionID, "turn")
	}); recordErr != nil {
		return replied, recordErr
	}
	if replied {
		return true, nil
	}
	return false, err
}

// notify sends the chat a short notice. A notice that cannot be sent is only
// logged: it never changes what happens to the task.
func (coordinator *Coordinator) notify(key agent.Key, text string, replyTo identity.MessageID) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := coordinator.messenger.SendText(ctx, key, text, replyTo); err != nil {
		coordinator.logger.Warn("sub-agent notice could not be sent", "error", err)
	}
}

func lastMessage(ids []identity.MessageID) identity.MessageID {
	if len(ids) == 0 {
		return identity.MessageID{}
	}
	return ids[len(ids)-1]
}

func (coordinator *Coordinator) inputDir(sessionID string) string {
	return filepath.Join(coordinator.config.DataDir, "in", pathKey(sessionID))
}

func (coordinator *Coordinator) outputDir(sessionID string) string {
	return filepath.Join(coordinator.config.DataDir, "out", pathKey(sessionID))
}

// resolveInputs turns the messages a task names into files under dir: each
// message's own file, and its text as user_message<N>.txt. A message that
// yields neither fails the whole set, so a task never starts without a file
// the model promised it.
func (coordinator *Coordinator) resolveInputs(ctx context.Context, key agent.Key, dir string, messageIDs []identity.MessageID) ([]string, error) {
	if len(messageIDs) == 0 {
		return nil, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	var files []string
	index := 1
	for _, messageID := range messageIDs {
		input, err := coordinator.inputs.MessageInput(ctx, key, messageID, dir)
		if err != nil {
			return nil, err
		}
		if len(input.Paths) == 0 && strings.TrimSpace(input.Text) == "" {
			return nil, agent.NewError(agent.ErrorNotFound, "read sub-agent input", errors.New("a requested message has no file and no text"))
		}
		for _, path := range input.Paths {
			info, err := os.Stat(path)
			if err != nil || !info.Mode().IsRegular() || info.Size() > MaxFileBytes {
				return nil, agent.NewError(agent.ErrorInvalidArgument, "read sub-agent input", errors.New("an input file is missing or too large"))
			}
			files = append(files, path)
			index++
		}
		if strings.TrimSpace(input.Text) != "" {
			path := filepath.Join(dir, fmt.Sprintf("user_message%d.txt", index))
			if err := os.WriteFile(path, []byte(input.Text), 0o600); err != nil {
				return nil, err
			}
			files = append(files, path)
			index++
		}
	}
	return files, nil
}

// pruneOutputs deletes received output directories older than
// outputRetention, and input directories left by a crash.
func (coordinator *Coordinator) pruneOutputs() {
	cutoff := time.Now().Add(-outputRetention)
	for _, name := range []string{"out", "in"} {
		root := filepath.Join(coordinator.config.DataDir, name)
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			info, err := entry.Info()
			if err != nil || info.ModTime().After(cutoff) {
				continue
			}
			_ = os.RemoveAll(filepath.Join(root, entry.Name()))
		}
	}
}

// statusBlock describes the chat's sub-agent task for the model: the running
// one, else one that just finished and is being delivered, else none.
func (coordinator *Coordinator) statusBlock(ctx context.Context, key agent.Key) string {
	if active, err := coordinator.store.ActiveTask(ctx, key); err == nil && active != nil {
		return coordinator.formatActive(*active)
	}
	if finished, err := coordinator.store.LatestUndelivered(ctx, key); err == nil && finished != nil {
		if age := coordinator.clock.Now().Sub(finished.EndedAt); age >= 0 && age <= recentFinishedAge {
			return formatRecentFinished(*finished)
		}
	}
	return "## Sub-agent status\n" +
		"No sub-agent task is currently running or recently finished.\n" +
		"If the user's request qualifies (file processing, code execution, " +
		"web scraping, etc.), call `execute_subtask` immediately."
}

const (
	// A task keeps up to maxProgressEntries entries of up to 500 characters;
	// the model is shown only the last few, shortened.
	contextProgressEntries = 5
	contextProgressChars   = 200
)

func (coordinator *Coordinator) formatActive(task Task) string {
	elapsed := max(0, coordinator.clock.Now().Sub(task.StartedAt))
	minutes, seconds := int(elapsed.Minutes()), int(elapsed.Seconds())%60
	running := fmt.Sprintf("%ds", seconds)
	if minutes > 0 {
		running = fmt.Sprintf("%dm %ds", minutes, seconds)
	}
	lines := []string{
		"## Active sub-agent task (already running for this chat)",
		"- Instruction: " + task.Instruction,
		"- Running for: " + running,
	}
	coordinator.mu.Lock()
	var progress []ProgressEntry
	if live := coordinator.live[task.SessionID]; live != nil {
		progress = append(progress, live.progress...)
	}
	coordinator.mu.Unlock()
	if total := len(progress); total > 0 {
		tail := progress[max(0, total-contextProgressEntries):]
		header := "Progress so far"
		if total > len(tail) {
			header += fmt.Sprintf(" (showing last %d of %d)", len(tail), total)
		}
		lines = append(lines, "", header+":")
		for _, entry := range tail {
			// The reason says what the step is for; older services send
			// only the detail.
			rendered := entry.Step + ": " + entry.Detail
			if reason := strings.TrimSpace(entry.Reason); reason != "" {
				rendered = entry.Step + ": " + reason
			}
			if runes := []rune(rendered); len(runes) > contextProgressChars {
				rendered = string(runes[:contextProgressChars-1]) + "…"
			}
			lines = append(lines, "- "+rendered)
		}
	}
	lines = append(lines, "",
		"Rules while a sub-agent is in flight:",
		"- You could call `execute_subtask` again for this chat for steering the sub-agent to correct its progress",
		`- DO NOT re-acknowledge with phrases like "i'll check it!" / "wait a minute." / "on it". The user already saw your earlier acknowledgement.`,
		`- If the user asks an unrelated question, answer that briefly. If they're just checking on progress, DO NOT reply with "It's still working...", they will think you made things up; instead, tell them the current progress of the sub-agent task.`,
		"- The sub-agent's final report will be delivered to you on the next turn as a `【SUBTASK FINISHED】` system message; that's when you summarise the result for the user.",
	)
	return strings.Join(lines, "\n")
}

func formatRecentFinished(task Task) string {
	success := "no"
	if task.Status == StatusCompleted {
		success = "yes"
	}
	lines := []string{
		"## Recently finished sub-agent task",
		"- Instruction: " + task.Instruction,
		"- Success: " + success,
	}
	if task.Report != "" {
		lines = append(lines, "- Report: "+task.Report)
	}
	lines = append(lines, "",
		"This task has already been delivered to the user (if there are any files attached to it).",
		"DO NOT call `execute_subtask` again for THIS SAME task — it is already done and delivered. "+
			"You MAY call it for a DIFFERENT new task the user explicitly asks for.",
	)
	return strings.Join(lines, "\n")
}
