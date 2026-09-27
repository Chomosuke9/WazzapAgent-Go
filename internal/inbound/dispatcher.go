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
)

type Options struct {
	Debounce    time.Duration
	BurstCap    uint32
	Activity    AIActivity
	Events      AgentLifecycleObserver
	ChatContext agent.ChatContextReader
	// Muter, when set, deletes group messages from muted senders.
	Muter MuteDeleter
	// Stickers, when set, lists the chat's sticker catalog for send_sticker.
	Stickers StickerLister
	Clock    agent.Clock
	// Report receives errors from turns that run after Handle returned.
	Report func(error)
}

type StickerLister interface {
	StickerNames(context.Context, agent.Key) ([]string, error)
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
	// overflow is set when a message was left in the inbox because pending
	// was full.
	overflow bool
	// refilling is set while the inbox is read back; arrivals meanwhile
	// stay in the inbox too.
	refilling bool
	timer     *time.Timer
	// timerSeq identifies the current timer, so a callback that fired just
	// before its timer was replaced does nothing.
	timerSeq uint64
	running  bool
	retries  int
}

func (queue *chatQueue) idle() bool {
	return queue.users == 0 && !queue.running && queue.timer == nil && len(queue.pending) == 0
}

// maxPending bounds how many messages one chat keeps in memory. Beyond it,
// messages stay in the durable inbox and are read back once the chat's queue
// drains. A variable only so tests can lower it.
var maxPending = 256

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

// Recover resumes every message the last run left unfinished, oldest first,
// reading the inbox one page at a time. It returns the first transient
// failure, so the caller can run it again; other failures are reported.
func (dispatcher *Dispatcher) Recover(ctx context.Context, tenantID identity.TenantID) error {
	var after *conversation.IncomingMessage
	var transient error
	for {
		messages, err := dispatcher.store.ListUnfinished(ctx, tenantID, after, maxPending)
		if err != nil {
			return err
		}
		for _, message := range messages {
			if ctx.Err() != nil {
				return nil
			}
			err := dispatcher.Resume(ctx, message)
			switch {
			case err == nil:
			case agent.RetryableGeneration(err):
				if transient == nil {
					transient = err
				}
			default:
				dispatcher.options.Report(err)
			}
		}
		if len(messages) < maxPending {
			return transient
		}
		after = &messages[len(messages)-1]
	}
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
	dispatcher.mu.Lock()
	queue := dispatcher.chat(key)
	queue.users++
	dispatcher.mu.Unlock()
	queue.commands.Lock()
	defer func() {
		queue.commands.Unlock()
		dispatcher.mu.Lock()
		queue.users--
		if queue.overflow && queue.users == 0 && !queue.running && queue.timer == nil &&
			len(queue.pending) == 0 && dispatcher.ctx.Err() == nil {
			dispatcher.refill(key, queue)
		} else {
			dispatcher.forgetIfIdle(key, queue)
		}
		dispatcher.mu.Unlock()
	}()
	// A command runs once: resumeCommand closes a failed one (failCommand).
	return dispatcher.resumeCommand(ctx, message, request, cmd)
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
	// Once a message is left in the inbox, later ones wait there too, so the
	// refill reads them back in arrival order.
	if queue.overflow || queue.refilling || len(queue.pending) >= maxPending {
		queue.overflow = true
		return nil
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
	queue.timerSeq++
	if dispatcher.ctx.Err() != nil {
		queue.timer = nil
		return
	}
	seq := queue.timerSeq
	queue.timer = time.AfterFunc(delay, func() { dispatcher.startTurn(key, queue, seq) })
}

func (dispatcher *Dispatcher) startTurn(key agent.Key, queue *chatQueue, seq uint64) {
	dispatcher.mu.Lock()
	if seq != queue.timerSeq {
		dispatcher.mu.Unlock()
		return // a newer schedule replaced this timer
	}
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
		rest, redo, err := dispatcher.runTurn(dispatcher.ctx, batch)
		dispatcher.mu.Lock()
		defer dispatcher.mu.Unlock()
		queue.running = false
		// Messages the claim did not consume go back to the front of the queue.
		requeue := rest
		retry := err != nil && len(redo) > 0 && dispatcher.ctx.Err() == nil &&
			queue.retries < maxGenerationRetries
		switch {
		case retry:
			// A failure other than a transient one is still reported; running
			// it again is harmless, since the claim skips an answered anchor.
			if !agent.RetryableGeneration(err) {
				dispatcher.options.Report(err)
			}
			queue.retries++
			requeue = append(redo, requeue...)
		case err != nil && dispatcher.ctx.Err() == nil:
			dispatcher.options.Report(err)
			queue.retries = 0
		default:
			queue.retries = 0
		}
		queue.pending = append(requeue, queue.pending...)
		if len(queue.pending) == 0 && queue.overflow && dispatcher.ctx.Err() == nil {
			queue.overflow = false
			dispatcher.refill(key, queue)
			return
		}
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

// runTurn claims batch and answers it. It returns the messages of batch the
// claim did not consume, and redo: what to run again when err is set.
// Once claimed, only the anchor still needs a reply; the rest of its batch is
// recorded as part of it.
func (dispatcher *Dispatcher) runTurn(ctx context.Context, batch []conversation.IncomingMessage) (rest, redo []conversation.IncomingMessage, err error) {
	messages, rest, err := dispatcher.store.ClaimBatch(ctx, batch)
	if err != nil {
		return nil, batch, err
	}
	if len(messages) == 0 {
		return rest, nil, nil
	}
	anchor := messages[len(messages)-1:]
	currentAgent, err := dispatcher.agents.AgentFor(ctx, agent.Key{
		TenantID: anchor[0].TenantID, AccountID: anchor[0].AccountID, ChatID: anchor[0].ChatID,
	})
	if err != nil {
		return rest, anchor, err
	}
	dispatcher.observer.ObserveInboundBatch(uint32(len(messages)))
	if err := dispatcher.processBatch(ctx, currentAgent, messages); err != nil {
		return rest, anchor, err
	}
	return rest, nil, nil
}

// refill reads back the messages that were left in the inbox while the
// chat's queue was full. Callers hold mu; the read runs outside it.
func (dispatcher *Dispatcher) refill(key agent.Key, queue *chatQueue) {
	queue.running = true // keeps the chat's turns serial while reading
	queue.refilling = true
	dispatcher.turns.Add(1)
	go func() {
		defer dispatcher.turns.Done()
		messages, err := dispatcher.readInbox(key)
		if err != nil && dispatcher.ctx.Err() == nil {
			// The next startup recovers what is left in the inbox.
			dispatcher.options.Report(err)
		}
		dispatcher.mu.Lock()
		defer dispatcher.mu.Unlock()
		queue.running = false
		queue.refilling = false
		missed := queue.overflow // an arrival landed after the read
		// A full page means more may still wait in the inbox.
		queue.overflow = len(messages) == maxPending
		for _, message := range messages {
			// A command still in the inbox is running right now on its own path.
			if IsCommand(message.Text) {
				continue
			}
			queue.pending = append(queue.pending, message)
		}
		if len(queue.pending) > 0 {
			queue.overflow = queue.overflow || missed
			dispatcher.schedule(key, queue, 0)
			return
		}
		switch {
		case err != nil || dispatcher.ctx.Err() != nil:
			queue.overflow = false
		case missed || queue.overflow && queue.users == 0:
			dispatcher.refill(key, queue)
			return
		case queue.overflow:
			// The page held only commands that are still running; the last
			// of them to finish reads the inbox again.
			return
		}
		dispatcher.forgetIfIdle(key, queue)
	}()
}

// readInbox reads one page of the chat's unfinished messages, trying a
// failed read again like a failed generation.
func (dispatcher *Dispatcher) readInbox(key agent.Key) ([]conversation.IncomingMessage, error) {
	for attempt := 0; ; attempt++ {
		messages, err := dispatcher.store.ListUnfinishedInChat(dispatcher.ctx, key, maxPending)
		if err == nil || attempt == maxGenerationRetries {
			return messages, err
		}
		select {
		case <-dispatcher.ctx.Done():
			return nil, err
		case <-time.After(generationRetryDelay):
		}
	}
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
