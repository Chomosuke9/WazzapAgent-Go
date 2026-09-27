package inbound

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

const (
	// maxGenerationRetries bounds how often a turn whose model call failed
	// (rate limit, timeout, outage) is generated again in this process.
	maxGenerationRetries = 2
	generationRetryDelay = 2 * time.Second
	// maxCommandAttempts bounds how often a command that failed only because
	// WhatsApp was not ready yet (still connecting, group data syncing) runs.
	maxCommandAttempts = 3
	commandRetryDelay  = 2 * time.Second
)

type Options struct {
	Debounce    time.Duration
	BurstCap    uint32
	Activity    AIActivity
	Events      AgentLifecycleObserver
	ChatContext agent.ChatContextReader
	// Muter, when set, deletes group messages from muted senders.
	Muter MuteDeleter
	Clock agent.Clock
	// Report receives errors from turns that run after Handle returned.
	Report func(error)
}

type MuteDeleter interface {
	DeleteMessage(context.Context, agent.Key, identity.MessageID) error
}

// Dispatcher routes each incoming message. Commands run on the caller's
// goroutine, one at a time per chat. AI messages wait in their chat's queue
// until the chat has been quiet for the debounce window, then run as one
// batch on a goroutine of their own; a chat never runs two turns at once.
// Nothing waits in a worker pool: a waiting chat is a timer.
type Dispatcher struct {
	handlerServices
	options Options

	ctx    context.Context // outlives Handle calls; cancelled by Run's exit
	cancel context.CancelFunc
	turns  sync.WaitGroup

	mu    sync.Mutex
	chats map[agent.Key]*chatQueue
}

type chatQueue struct {
	commands sync.Mutex // one command at a time in this chat
	users    int        // callers holding or waiting on commands

	pending []conversation.IncomingMessage
	timer   *time.Timer
	running bool
	retries int
}

func (queue *chatQueue) idle() bool {
	return queue.users == 0 && !queue.running && queue.timer == nil && len(queue.pending) == 0
}

func NewDispatcher(
	store Store,
	agents Registry,
	policy Policy,
	responses ResponseWriter,
	observer Observer,
	platform command.Platform,
	options Options,
) (*Dispatcher, error) {
	if store == nil || agents == nil || policy == nil || responses == nil || observer == nil {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "create inbound dispatcher", errors.New("store, registry, policy, response writer, and observer are required"))
	}
	if options.Debounce < 0 || options.Debounce > time.Minute || options.BurstCap == 0 || options.BurstCap > 256 {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "create inbound dispatcher", errors.New("valid batching bounds are required"))
	}
	if options.Activity == nil {
		options.Activity = discardAIActivity{}
	}
	if options.Events == nil {
		options.Events = discardAgentLifecycleObserver{}
	}
	if options.Clock == nil {
		options.Clock = agent.SystemClock{}
	}
	if options.Report == nil {
		options.Report = func(error) {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Dispatcher{
		handlerServices: handlerServices{store: store, agents: agents, policy: policy, responses: responses, observer: observer, platform: platform},
		options:         options,
		ctx:             ctx,
		cancel:          cancel,
		chats:           make(map[agent.Key]*chatQueue),
	}, nil
}

// Run waits for ctx, then stops starting turns and waits for running ones.
func (dispatcher *Dispatcher) Run(ctx context.Context) error {
	<-ctx.Done()
	dispatcher.cancel()
	dispatcher.mu.Lock()
	for _, queue := range dispatcher.chats {
		if queue.timer != nil {
			queue.timer.Stop()
			queue.timer = nil
		}
	}
	dispatcher.mu.Unlock()
	dispatcher.turns.Wait()
	return nil
}

// Recover resumes every message the last run left unfinished, oldest first.
func (dispatcher *Dispatcher) Recover(ctx context.Context, tenantID identity.TenantID) error {
	messages, err := dispatcher.store.ListUnfinished(ctx, tenantID)
	if err != nil {
		return err
	}
	for _, message := range messages {
		if ctx.Err() != nil {
			return nil
		}
		if err := dispatcher.Resume(ctx, message); err != nil {
			dispatcher.options.Report(err)
		}
	}
	return nil
}

func (dispatcher *Dispatcher) Handle(ctx context.Context, candidate conversation.IncomingCandidate) error {
	if err := candidate.Validate(); err != nil {
		return agent.NewError(agent.ErrorInvalidArgument, "handle incoming candidate", err)
	}
	claimed, err := dispatcher.store.ClaimAndResolveSender(ctx, candidate)
	if err != nil {
		return err
	}
	if claimed.Duplicate {
		dispatcher.observer.ObserveInboundDuplicate()
	} else {
		dispatcher.observer.ObserveInboundClaimed()
	}
	if claimed.Handled {
		return nil
	}
	return dispatcher.Resume(ctx, claimed.Message)
}

// Resume routes a stored message. A command runs before Resume returns; an
// AI message is queued and Resume returns at once.
func (dispatcher *Dispatcher) Resume(ctx context.Context, message conversation.IncomingMessage) error {
	if err := message.Validate(); err != nil {
		return agent.NewError(agent.ErrorInvalidArgument, "route incoming message", err)
	}
	key := agent.Key{TenantID: message.TenantID, AccountID: message.AccountID, ChatID: message.ChatID}
	if dispatcher.options.Muter != nil && message.ChatKind == conversation.ChatGroup && !message.FromMe {
		muted, err := dispatcher.store.IsChatMuted(ctx, key, message.SenderRef, dispatcher.options.Clock.Now())
		if err != nil {
			return err
		}
		if muted {
			if err := dispatcher.options.Muter.DeleteMessage(ctx, key, message.ID); err != nil {
				return err
			}
			return dispatcher.ignore(ctx, message, IgnoreMuted)
		}
	}
	if request, cmd, recognized := parseRegisteredCommand(message.Text); recognized {
		return dispatcher.runCommand(ctx, key, message, request, cmd)
	}
	return dispatcher.queueAI(ctx, key, message)
}

func (dispatcher *Dispatcher) runCommand(ctx context.Context, key agent.Key, message conversation.IncomingMessage, request command.Request, cmd command.Command) error {
	// FromMe command messages intentionally run here too. The command's
	// permission expression decides whether the bot may run them; the AI
	// path still ignores ordinary FromMe messages to prevent self-replies.
	switch {
	case message.ChatKind == conversation.ChatStatus:
		return dispatcher.ignore(ctx, message, IgnoreStatus)
	case !message.Allowlisted:
		return dispatcher.ignore(ctx, message, IgnoreNotAllowlisted)
	}
	return dispatcher.runCommandAttempt(ctx, key, message, request, cmd, 1)
}

// runCommandAttempt runs a command once. A command that failed is recorded
// as failed and never runs again; only "not ready" gets a few more tries,
// on a timer so no caller waits.
func (dispatcher *Dispatcher) runCommandAttempt(ctx context.Context, key agent.Key, message conversation.IncomingMessage, request command.Request, cmd command.Command, attempt int) error {
	dispatcher.mu.Lock()
	queue := dispatcher.chat(key)
	queue.users++
	dispatcher.mu.Unlock()
	queue.commands.Lock()
	err := dispatcher.resumeCommand(ctx, message, request, cmd)
	queue.commands.Unlock()
	dispatcher.mu.Lock()
	retry := err != nil && agent.IsCode(err, agent.ErrorNotReady) && attempt < maxCommandAttempts && dispatcher.ctx.Err() == nil
	if retry {
		// The chat's queue stays alive while the retry waits.
		dispatcher.turns.Add(1)
		time.AfterFunc(commandRetryDelay, func() {
			defer dispatcher.turns.Done()
			if dispatcher.ctx.Err() == nil {
				if err := dispatcher.runCommandAttempt(dispatcher.ctx, key, message, request, cmd, attempt+1); err != nil {
					dispatcher.options.Report(err)
				}
			}
			dispatcher.mu.Lock()
			queue.users--
			dispatcher.forgetIfIdle(key, queue)
			dispatcher.mu.Unlock()
		})
	} else {
		queue.users--
		dispatcher.forgetIfIdle(key, queue)
	}
	dispatcher.mu.Unlock()
	if err == nil || retry {
		return nil
	}
	recordCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// A command that already recorded its reply is past this point; the
	// conflict that MarkIgnored reports for it changes nothing.
	_ = dispatcher.store.MarkIgnored(recordCtx, message, IgnoreCommandFailed)
	return err
}

func (dispatcher *Dispatcher) queueAI(ctx context.Context, key agent.Key, message conversation.IncomingMessage) error {
	switch {
	case message.FromMe:
		return dispatcher.ignore(ctx, message, IgnoreFromMe)
	case message.ChatKind == conversation.ChatStatus:
		return dispatcher.ignore(ctx, message, IgnoreStatus)
	case !message.Allowlisted:
		return dispatcher.ignore(ctx, message, IgnoreNotAllowlisted)
	}
	_, snapshot, err := dispatcher.loadAgent(ctx, message)
	if err != nil {
		return err
	}
	if err := dispatcher.policy.AuthorizeInvocation(ctx, message, snapshot); err != nil {
		return dispatcher.ignore(ctx, message, IgnorePolicyDenied)
	}
	if resumed, err := dispatcher.responses.Resume(ctx, message); err != nil || resumed {
		return err
	}
	dispatcher.mu.Lock()
	defer dispatcher.mu.Unlock()
	queue := dispatcher.chat(key)
	for _, queued := range queue.pending {
		if queued.InvocationID == message.InvocationID {
			return nil
		}
	}
	queue.pending = append(queue.pending, message)
	if !queue.running {
		delay := dispatcher.options.Debounce
		if uint32(len(queue.pending)) >= dispatcher.options.BurstCap {
			delay = 0
		}
		// Each new message restarts the quiet window.
		dispatcher.schedule(key, queue, delay)
	}
	return nil
}

// schedule starts the chat's next turn after delay. Callers hold mu.
func (dispatcher *Dispatcher) schedule(key agent.Key, queue *chatQueue, delay time.Duration) {
	if queue.timer != nil {
		queue.timer.Stop()
	}
	if dispatcher.ctx.Err() != nil {
		queue.timer = nil
		return
	}
	queue.timer = time.AfterFunc(delay, func() { dispatcher.startTurn(key, queue) })
}

func (dispatcher *Dispatcher) startTurn(key agent.Key, queue *chatQueue) {
	dispatcher.mu.Lock()
	queue.timer = nil
	if queue.running || len(queue.pending) == 0 || dispatcher.ctx.Err() != nil {
		dispatcher.forgetIfIdle(key, queue)
		dispatcher.mu.Unlock()
		return
	}
	size := min(len(queue.pending), int(dispatcher.options.BurstCap))
	batch := append([]conversation.IncomingMessage(nil), queue.pending[:size]...)
	queue.pending = append([]conversation.IncomingMessage(nil), queue.pending[size:]...)
	queue.running = true
	dispatcher.turns.Add(1)
	dispatcher.mu.Unlock()

	go func() {
		defer dispatcher.turns.Done()
		used, anchor, err := dispatcher.runTurn(dispatcher.ctx, batch)
		dispatcher.mu.Lock()
		defer dispatcher.mu.Unlock()
		queue.running = false
		// Messages the claim did not consume go back to the front of the queue.
		requeue := append([]conversation.IncomingMessage(nil), batch[used:]...)
		retry := err != nil && anchor != nil && agent.RetryableGeneration(err) && dispatcher.ctx.Err() == nil &&
			queue.retries < maxGenerationRetries
		switch {
		case retry:
			// Only the anchor still needs a reply; the rest of its batch is
			// already recorded as part of it.
			queue.retries++
			requeue = append([]conversation.IncomingMessage{*anchor}, requeue...)
		case err != nil && dispatcher.ctx.Err() == nil:
			dispatcher.options.Report(err)
			queue.retries = 0
		default:
			queue.retries = 0
		}
		queue.pending = append(requeue, queue.pending...)
		if len(queue.pending) > 0 {
			delay := time.Duration(0)
			if retry {
				delay = generationRetryDelay
			}
			dispatcher.schedule(key, queue, delay)
			return
		}
		dispatcher.forgetIfIdle(key, queue)
	}()
}

// runTurn claims batch and answers it. It returns how many messages of batch
// were consumed and the anchor that was answered, if any. A batch that
// cannot be claimed is dropped here; the next startup recovers it.
func (dispatcher *Dispatcher) runTurn(ctx context.Context, batch []conversation.IncomingMessage) (int, *conversation.IncomingMessage, error) {
	messages, used, err := dispatcher.store.ClaimBatch(ctx, batch)
	if err != nil {
		return len(batch), nil, err
	}
	if len(messages) == 0 {
		return used, nil, nil
	}
	anchor := messages[len(messages)-1]
	currentAgent, err := dispatcher.agents.AgentFor(ctx, agent.Key{
		TenantID: anchor.TenantID, AccountID: anchor.AccountID, ChatID: anchor.ChatID,
	})
	if err != nil {
		return used, nil, err
	}
	dispatcher.observer.ObserveInboundBatch(uint32(len(messages)))
	return used, &anchor, dispatcher.processBatch(ctx, currentAgent, messages)
}

// chat returns the chat's queue, creating it. Callers hold mu.
func (dispatcher *Dispatcher) chat(key agent.Key) *chatQueue {
	queue := dispatcher.chats[key]
	if queue == nil {
		queue = &chatQueue{}
		dispatcher.chats[key] = queue
	}
	return queue
}

// forgetIfIdle drops a chat that has nothing queued, running, or waiting.
// Callers hold mu.
func (dispatcher *Dispatcher) forgetIfIdle(key agent.Key, queue *chatQueue) {
	if queue.idle() && dispatcher.chats[key] == queue {
		delete(dispatcher.chats, key)
	}
}

func IsCommand(text string) bool {
	_, _, recognized := parseRegisteredCommand(text)
	return recognized
}
