package inbound

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

type Lane string

const (
	LaneCommand Lane = "command"
	LaneAI      Lane = "ai"
)

type laneResumer interface {
	Resume(context.Context, conversation.IncomingMessage) error
}

type CommandHandler struct {
	handlerServices
}

type AIHandler struct {
	handlerServices
	batch BatchOptions
}

func newHandlerServices(
	store Store,
	agents Registry,
	policy Policy,
	responses ResponseWriter,
	observer Observer,
) (handlerServices, error) {
	if store == nil || agents == nil || policy == nil || responses == nil || observer == nil {
		return handlerServices{}, agent.NewError(agent.ErrorInvalidArgument, "create inbound lane services", fmt.Errorf("store, registry, policy, response writer, and observer are required"))
	}
	return handlerServices{store: store, agents: agents, policy: policy, responses: responses, observer: observer}, nil
}

func newCommandHandler(services handlerServices) (*CommandHandler, error) {
	services.stripes = new([64]sync.Mutex)
	return &CommandHandler{handlerServices: services}, nil
}

func newAIHandler(services handlerServices, options BatchOptions) (*AIHandler, error) {
	if options.Debounce < 0 || options.Debounce > time.Minute || options.BurstCap == 0 || options.BurstCap > 256 || options.Clock == nil {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "create AI handler", fmt.Errorf("valid batching bounds and clock are required"))
	}
	if options.Activity == nil {
		options.Activity = discardAIActivity{}
	}
	services.stripes = new([64]sync.Mutex)
	return &AIHandler{handlerServices: services, batch: options}, nil
}

// NewCommandHandler constructs the command lane without constructing or
// depending on the AI lane.
func NewCommandHandler(
	store Store,
	agents Registry,
	policy Policy,
	responses ResponseWriter,
	observer Observer,
) (*CommandHandler, error) {
	services, err := newHandlerServices(store, agents, policy, responses, observer)
	if err != nil {
		return nil, err
	}
	return newCommandHandler(services)
}

// NewAIHandler constructs the AI lane without constructing or depending on
// the command lane.
func NewAIHandler(
	store Store,
	agents Registry,
	policy Policy,
	responses ResponseWriter,
	observer Observer,
	options BatchOptions,
) (*AIHandler, error) {
	services, err := newHandlerServices(store, agents, policy, responses, observer)
	if err != nil {
		return nil, err
	}
	return newAIHandler(services, options)
}

func (handler *CommandHandler) Resume(ctx context.Context, message conversation.IncomingMessage) error {
	if err := message.Validate(); err != nil {
		return agent.NewError(agent.ErrorInvalidArgument, "resume command message", err)
	}
	if !IsCommand(message.Text) {
		return agent.NewError(agent.ErrorInvalidArgument, "run command handler", fmt.Errorf("message is not a registered command"))
	}
	switch {
	case message.FromMe:
		return handler.ignore(ctx, message, IgnoreFromMe)
	case message.ChatKind == conversation.ChatStatus:
		return handler.ignore(ctx, message, IgnoreStatus)
	case !message.Allowlisted:
		return handler.ignore(ctx, message, IgnoreNotAllowlisted)
	}
	request, descriptor, recognized := parseRegisteredCommand(message.Text)
	if !recognized {
		return agent.NewError(agent.ErrorIntegrityFailure, "run command handler", fmt.Errorf("registered command disappeared during dispatch"))
	}
	return handler.resumeCommand(ctx, message, request, descriptor)
}

func (handler *AIHandler) Resume(ctx context.Context, message conversation.IncomingMessage) error {
	if err := message.Validate(); err != nil {
		return agent.NewError(agent.ErrorInvalidArgument, "resume AI message", err)
	}
	if IsCommand(message.Text) {
		return agent.NewError(agent.ErrorInvalidArgument, "run AI handler", fmt.Errorf("registered command cannot enter AI lane"))
	}
	switch {
	case message.FromMe:
		return handler.ignore(ctx, message, IgnoreFromMe)
	case message.ChatKind == conversation.ChatStatus:
		return handler.ignore(ctx, message, IgnoreStatus)
	case !message.Allowlisted:
		return handler.ignore(ctx, message, IgnoreNotAllowlisted)
	case message.ChatKind == conversation.ChatGroup && !message.MentionsBot && !message.RepliedToBot:
		return handler.ignore(ctx, message, IgnoreGroupNotMentioned)
	}

	currentAgent, snapshot, err := handler.loadAgent(ctx, message)
	if err != nil {
		return err
	}
	if err := handler.policy.AuthorizeInvocation(ctx, message, snapshot.Permission); err != nil {
		return handler.ignore(ctx, message, IgnorePolicyDenied)
	}
	if resumed, err := handler.responses.Resume(ctx, message); err != nil || resumed {
		return err
	}
	stage, err := handler.store.StageBatch(ctx, message, handler.batch.Clock.Now().Add(handler.batch.Debounce))
	if err != nil || stage.Handled || !stage.Wait {
		return err
	}
	readyAt := stage.ReadyAt
	for {
		if err := handler.waitUntil(ctx, readyAt); err != nil {
			return err
		}
		stripe := handler.stripe(message.ChatID)
		stripe.Lock()
		claim, claimErr := handler.store.ClaimBatch(ctx, message, handler.batch.Clock.Now(), handler.batch.BurstCap)
		if claimErr != nil {
			stripe.Unlock()
			return claimErr
		}
		if claim.Handled {
			stripe.Unlock()
			return nil
		}
		if !claim.ReadyAt.IsZero() {
			readyAt = claim.ReadyAt
			stripe.Unlock()
			continue
		}
		handler.observer.ObserveInboundBatch(uint32(len(claim.Messages)))
		err = handler.processBatch(ctx, currentAgent, claim.Messages)
		stripe.Unlock()
		if err != nil {
			return err
		}
		readyAt = handler.batch.Clock.Now()
	}
}

// SplitDispatcher owns the durable intake boundary and two independent
// execution pools. A stalled model call can consume AI workers only; command
// workers and their queue remain available.
type SplitDispatcher struct {
	store          Store
	command        laneResumer
	ai             laneResumer
	observer       Observer
	commandQueue   chan conversation.IncomingMessage
	aiQueue        chan conversation.IncomingMessage
	commandWorkers uint32
	aiWorkers      uint32
	report         func(Lane, error)
	muteDeleter    MuteDeleter
	clock          agent.Clock
}

type MuteDeleter interface {
	DeleteMessage(context.Context, agent.Key, identity.MessageID) error
}

func NewSplitDispatcher(store Store, command, ai laneResumer, observer Observer, commandQueue, aiQueue, commandWorkers, aiWorkers uint32, report func(Lane, error)) (*SplitDispatcher, error) {
	if store == nil || command == nil || ai == nil || observer == nil || commandQueue == 0 || aiQueue == 0 || commandWorkers == 0 || aiWorkers == 0 {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "create split inbound dispatcher", fmt.Errorf("store, lanes, observer, queues, and workers are required"))
	}
	if report == nil {
		report = func(Lane, error) {}
	}
	return &SplitDispatcher{
		store: store, command: command, ai: ai, observer: observer,
		commandQueue:   make(chan conversation.IncomingMessage, commandQueue),
		aiQueue:        make(chan conversation.IncomingMessage, aiQueue),
		commandWorkers: commandWorkers, aiWorkers: aiWorkers, report: report, clock: agent.SystemClock{},
	}, nil
}

func (dispatcher *SplitDispatcher) EnableMuteEnforcement(deleter MuteDeleter, clock agent.Clock) error {
	if deleter == nil || clock == nil {
		return agent.NewError(agent.ErrorInvalidArgument, "enable mute enforcement", fmt.Errorf("deleter and clock are required"))
	}
	dispatcher.muteDeleter, dispatcher.clock = deleter, clock
	return nil
}

func (dispatcher *SplitDispatcher) Handle(ctx context.Context, candidate conversation.IncomingCandidate) error {
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

func (dispatcher *SplitDispatcher) Resume(ctx context.Context, message conversation.IncomingMessage) error {
	if err := message.Validate(); err != nil {
		return agent.NewError(agent.ErrorInvalidArgument, "route incoming message", err)
	}
	if dispatcher.muteDeleter != nil && message.ChatKind == conversation.ChatGroup && !message.FromMe {
		key := agent.Key{TenantID: message.TenantID, AccountID: message.AccountID, ChatID: message.ChatID}
		muted, err := dispatcher.store.IsChatMuted(ctx, key, message.SenderRef, dispatcher.clock.Now())
		if err != nil {
			return err
		}
		if muted {
			if err := dispatcher.muteDeleter.DeleteMessage(ctx, key, message.ID); err != nil {
				return err
			}
			if err := dispatcher.store.MarkIgnored(ctx, message, IgnoreMuted); err != nil {
				return err
			}
			dispatcher.observer.ObserveInboundIgnored()
			return nil
		}
	}
	queue := dispatcher.aiQueue
	if IsCommand(message.Text) {
		queue = dispatcher.commandQueue
	}
	// The message is already durable. Never let saturation in one lane block
	// intake for the other; recovery will enqueue it after capacity returns.
	select {
	case <-ctx.Done():
		return agent.NewError(agent.ErrorCancelled, "route incoming message", ctx.Err())
	case queue <- message:
		return nil
	default:
		return nil
	}
}

func (dispatcher *SplitDispatcher) Run(ctx context.Context) error {
	var workers sync.WaitGroup
	start := func(lane Lane, count uint32, queue <-chan conversation.IncomingMessage, handler laneResumer) {
		for index := uint32(0); index < count; index++ {
			workers.Add(1)
			go func() {
				defer workers.Done()
				for {
					select {
					case <-ctx.Done():
						return
					case message := <-queue:
						if err := handler.Resume(ctx, message); err != nil && ctx.Err() == nil {
							dispatcher.report(lane, err)
						}
					}
				}
			}()
		}
	}
	start(LaneCommand, dispatcher.commandWorkers, dispatcher.commandQueue, dispatcher.command)
	start(LaneAI, dispatcher.aiWorkers, dispatcher.aiQueue, dispatcher.ai)
	<-ctx.Done()
	workers.Wait()
	return nil
}

func IsCommand(text string) bool {
	_, _, recognized := parseRegisteredCommand(text)
	return recognized
}
