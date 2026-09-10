package inbound

import (
	"context"
	"fmt"
	"sync"

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

type CommandHandler struct{ core *Handler }
type AIHandler struct{ core *Handler }

func NewCommandHandler(core *Handler) (*CommandHandler, error) {
	if core == nil {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "create command handler", fmt.Errorf("core handler is required"))
	}
	return &CommandHandler{core: core}, nil
}

func NewAIHandler(core *Handler) (*AIHandler, error) {
	if core == nil {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "create AI handler", fmt.Errorf("core handler is required"))
	}
	return &AIHandler{core: core}, nil
}

func (handler *CommandHandler) Resume(ctx context.Context, message conversation.IncomingMessage) error {
	if !IsCommand(message.Text) {
		return agent.NewError(agent.ErrorInvalidArgument, "run command handler", fmt.Errorf("message is not a command"))
	}
	return handler.core.Resume(ctx, message)
}

func (handler *AIHandler) Resume(ctx context.Context, message conversation.IncomingMessage) error {
	if IsCommand(message.Text) {
		return agent.NewError(agent.ErrorInvalidArgument, "run AI handler", fmt.Errorf("command cannot enter AI lane"))
	}
	return handler.core.Resume(ctx, message)
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
