package discord

import (
	"context"
	"errors"
	"hash/fnv"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/Chomosuke9/DiscordAgent-Go/internal/account"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/agent"
	broadcastmodel "github.com/Chomosuke9/DiscordAgent-Go/internal/broadcast"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/control"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/conversation"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/identity"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/policy"
)

const sendStripeCount = 64

type CandidateHandler interface {
	Handle(context.Context, conversation.IncomingCandidate) error
}

// TargetStore maps the core's identities back to Discord IDs.
type TargetStore interface {
	ResolveChatAddress(context.Context, agent.Key) (string, error)
	ResolveMessageTarget(context.Context, agent.Key, identity.MessageID) (chatAddress, providerMessageID, senderAddress string, occurredAt time.Time, err error)
	ReconcileAccountPolicy(context.Context, identity.TenantID, identity.AccountID, string, []string) error
	ResolveUserID(context.Context, agent.Key, identity.SenderRef) (identity.UserID, error)
	SetChatMute(context.Context, agent.Key, identity.SenderRef, uint32, time.Time) error
}

// ChannelNameStore keeps the names of known server channels current.
type ChannelNameStore interface {
	SaveGroupName(context.Context, identity.TenantID, identity.AccountID, string, string) error
}

// SentStore records what the bot sent outside the reply outbox, so a reply
// to it still counts as a reply to the bot.
type SentStore interface {
	RecordSentSticker(ctx context.Context, key agent.Key, providerReceipt, name string) error
	RecordReceiptAliases(ctx context.Context, key agent.Key, primary string, aliases []string) error
}

type Config struct {
	TenantID       identity.TenantID
	AccountID      identity.AccountID
	Token          string
	OwnerAddress   string
	Allowlist      []string
	QueueCapacity  uint32
	Workers        uint32
	ConnectTimeout time.Duration
	SendTimeout    time.Duration
	Targets        TargetStore
	ChannelNames   ChannelNameStore
	Broadcasts     broadcastmodel.Store
	Sent           SentStore
	Logger         *slog.Logger
}

type Adapter struct {
	tenantID       identity.TenantID
	accountID      identity.AccountID
	token          string
	gate           policy.InboundGate
	connectTimeout time.Duration
	sendTimeout    time.Duration
	targets        TargetStore
	channelNames   ChannelNameStore
	broadcasts     broadcastmodel.Store
	sent           SentStore
	handler        CandidateHandler
	logger         *slog.Logger
	client         *discordgo.Session
	botID          atomic.Value // string
	queue          chan conversation.IncomingCandidate
	workers        uint32

	namesMu    sync.Mutex
	savedNames map[string]string

	typingMu sync.Mutex
	typing   map[string]context.CancelFunc

	memberHandlesMu sync.Mutex
	memberHandles   map[string]memberHandleSet
	broadcastMu     sync.Mutex
	broadcastSet    broadcastHandleSet

	ready    atomic.Bool
	started  atomic.Bool
	closed   atomic.Bool
	checking atomic.Bool
	events   chan account.ConnectionEvent
	fatal    chan error
	rootCtx  context.Context
	cancel   context.CancelFunc
	removers []func()
	wait     sync.WaitGroup
	stopOnce sync.Once
	stopDone chan struct{}
	stopErr  error
	stripes  [sendStripeCount]sync.Mutex
}

// Open validates the configuration and prepares the client. It does not
// connect: Start does.
func Open(ctx context.Context, config Config) (*Adapter, error) {
	if config.TenantID.IsZero() || config.AccountID.IsZero() || config.Targets == nil {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "open Discord adapter", errors.New("identity and target resolver are required"))
	}
	if strings.TrimSpace(config.Token) == "" {
		return nil, agent.NewError(agent.ErrorNotReady, "open Discord adapter", errors.New("link a Discord bot in the app, or set DISCORDAGENT_DISCORD_TOKEN, before starting the Agent"))
	}
	if config.QueueCapacity == 0 || config.Workers == 0 || config.ConnectTimeout <= 0 || config.SendTimeout <= 0 {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "open Discord adapter", errors.New("positive queue, worker, and timeout values are required"))
	}
	owner := strings.TrimSpace(config.OwnerAddress)
	if _, err := identity.ParseUserID(owner); err != nil {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "normalize configured owner", err)
	}
	allowlist := make([]string, 0, len(config.Allowlist))
	for _, raw := range config.Allowlist {
		value := strings.TrimSpace(raw)
		if !policy.IsChatAllowlistWildcard(value) {
			if _, err := identity.ParseUserID(value); err != nil {
				return nil, agent.NewError(agent.ErrorInvalidArgument, "normalize configured allowlist", errors.New("allowlist entries must be Discord IDs or wildcards"))
			}
		}
		allowlist = append(allowlist, value)
	}
	gate, err := policy.NewInboundGate(owner, allowlist)
	if err != nil {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "open Discord adapter", err)
	}
	if err := config.Targets.ReconcileAccountPolicy(ctx, config.TenantID, config.AccountID, owner, gate.Allowlist()); err != nil {
		return nil, err
	}
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	client, err := newClient(config.Token, agentIntents, logger)
	if err != nil {
		return nil, err
	}
	adapter := &Adapter{
		tenantID: config.TenantID, accountID: config.AccountID, token: config.Token, gate: gate,
		connectTimeout: config.ConnectTimeout, sendTimeout: config.SendTimeout,
		targets: config.Targets, channelNames: config.ChannelNames, broadcasts: config.Broadcasts, sent: config.Sent,
		logger: logger, client: client,
		queue: make(chan conversation.IncomingCandidate, config.QueueCapacity), workers: config.Workers,
		savedNames: make(map[string]string), typing: make(map[string]context.CancelFunc),
		memberHandles: make(map[string]memberHandleSet),
		events:        make(chan account.ConnectionEvent, 8), fatal: make(chan error, 1),
	}
	adapter.botID.Store(control.BotIDFromToken(config.Token))
	return adapter, nil
}

func (adapter *Adapter) BindHandler(handler CandidateHandler) error {
	if handler == nil {
		return agent.NewError(agent.ErrorInvalidArgument, "bind Discord handler", errors.New("candidate handler is required"))
	}
	if adapter.started.Load() || adapter.handler != nil {
		return agent.NewError(agent.ErrorConflict, "bind Discord handler", errors.New("handler is already bound or adapter has started"))
	}
	adapter.handler = handler
	return nil
}

func (adapter *Adapter) Start(ctx context.Context) error {
	if adapter.closed.Load() {
		return agent.NewError(agent.ErrorNotReady, "start Discord adapter", errors.New("adapter is closed"))
	}
	if adapter.handler == nil {
		return agent.NewError(agent.ErrorInvalidArgument, "start Discord adapter", errors.New("candidate handler must be bound first"))
	}
	if !adapter.started.CompareAndSwap(false, true) {
		return agent.NewError(agent.ErrorConflict, "start Discord adapter", errors.New("adapter is already started"))
	}
	adapter.rootCtx, adapter.cancel = context.WithCancel(ctx)
	verifyCtx, cancelVerify := context.WithTimeout(adapter.rootCtx, adapter.connectTimeout)
	user, err := verifyBot(verifyCtx, adapter.client)
	cancelVerify()
	if err != nil {
		adapter.stopAfterFailedStart()
		return err
	}
	adapter.botID.Store(user.ID)
	adapter.removers = append(adapter.removers,
		adapter.client.AddHandler(adapter.onConnect),
		adapter.client.AddHandler(adapter.onDisconnect),
		adapter.client.AddHandler(adapter.onMessageCreate),
		adapter.client.AddHandler(adapter.onInteractionCreate),
		adapter.client.AddHandler(adapter.onGuildCreate),
		adapter.client.AddHandler(adapter.onChannelUpdate),
		adapter.client.AddHandler(adapter.onGuildStickersUpdate),
	)
	for index := uint32(0); index < adapter.workers; index++ {
		adapter.wait.Add(1)
		go adapter.worker()
	}
	if err := openGateway(adapter.rootCtx, adapter.client, adapter.connectTimeout); err != nil {
		adapter.stopAfterFailedStart()
		return err
	}
	adapter.ready.Store(true)
	adapter.emitConnection(account.ConnectionEvent{Connected: true, Code: "open"})
	if adapter.broadcasts != nil {
		adapter.wait.Add(1)
		go adapter.broadcastScheduleWorker()
	}
	return nil
}

func (adapter *Adapter) Stop(ctx context.Context) error {
	// One shutdown owns the workers and the gateway even if a caller times
	// out. The account runtime calls Stop only after Start has returned.
	adapter.stopOnce.Do(func() {
		adapter.stopDone = make(chan struct{})
		adapter.closed.Store(true)
		adapter.ready.Store(false)
		adapter.clearMemberHandles()
		go func() {
			defer close(adapter.stopDone)
			if adapter.started.Swap(false) {
				if adapter.cancel != nil {
					adapter.cancel()
				}
				adapter.removeHandlers()
				if err := adapter.client.Close(); err != nil && !errors.Is(err, discordgo.ErrWSNotFound) {
					adapter.logger.Debug("close Discord gateway", "error", err)
				}
			}
			adapter.stopAllTyping()
			adapter.wait.Wait()
		}()
	})
	select {
	case <-adapter.stopDone:
		return adapter.stopErr
	default:
	}
	select {
	case <-adapter.stopDone:
		return adapter.stopErr
	case <-ctx.Done():
		return agent.NewError(agent.ErrorTimeout, "stop Discord adapter", ctx.Err())
	}
}

func (adapter *Adapter) Ready() bool                            { return !adapter.closed.Load() && adapter.ready.Load() }
func (adapter *Adapter) Events() <-chan account.ConnectionEvent { return adapter.events }
func (adapter *Adapter) Fatal() <-chan error                    { return adapter.fatal }
func (adapter *Adapter) QueueUsage() (int, int)                 { return len(adapter.queue), cap(adapter.queue) }

func (adapter *Adapter) currentBotID() string {
	value, _ := adapter.botID.Load().(string)
	return value
}

func (adapter *Adapter) onConnect(_ *discordgo.Session, _ *discordgo.Connect) {
	if adapter.closed.Load() {
		return
	}
	adapter.ready.Store(true)
	adapter.logger.Info("Discord bot connected")
	adapter.emitConnection(account.ConnectionEvent{Connected: true, Code: "open"})
}

// onDisconnect reports the drop; discordgo reconnects by itself. A token
// reset in the Developer Portal never reconnects, so the drop also checks
// whether Discord still accepts the token.
func (adapter *Adapter) onDisconnect(_ *discordgo.Session, _ *discordgo.Disconnect) {
	if adapter.closed.Load() {
		return
	}
	adapter.ready.Store(false)
	adapter.logger.Warn("Discord bot disconnected; reconnecting")
	adapter.emitConnection(account.ConnectionEvent{Connected: false, Code: "reconnecting"})
	if !adapter.checking.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer adapter.checking.Store(false)
		ctx, cancel := context.WithTimeout(adapter.rootCtx, adapter.sendTimeout)
		defer cancel()
		if _, err := verifyBot(ctx, adapter.client); isRevokedToken(err) {
			adapter.emitFatal(agent.NewError(agent.ErrorUnavailable, "Discord bot token revoked", err))
		}
	}()
}

func (adapter *Adapter) onMessageCreate(_ *discordgo.Session, event *discordgo.MessageCreate) {
	if event == nil || event.Message == nil || adapter.rootCtx == nil {
		return
	}
	candidate, ok := adapter.candidateFromMessage(adapter.rootCtx, event.Message)
	if !ok {
		return
	}
	adapter.enqueue(candidate)
}

func (adapter *Adapter) onInteractionCreate(client *discordgo.Session, event *discordgo.InteractionCreate) {
	if event == nil || event.Interaction == nil || event.Type != discordgo.InteractionMessageComponent || adapter.rootCtx == nil {
		return
	}
	// Discord needs an answer within three seconds or it shows the tap as
	// failed. The answer only acknowledges the tap; the reply comes through
	// the normal pipeline.
	ackCtx, cancel := context.WithTimeout(adapter.rootCtx, 3*time.Second)
	err := client.InteractionRespond(event.Interaction, &discordgo.InteractionResponse{Type: discordgo.InteractionResponseDeferredMessageUpdate}, discordgo.WithContext(ackCtx))
	cancel()
	if err != nil {
		adapter.logger.Warn("Discord tap could not be acknowledged", "code", agent.CodeOf(providerError(ackCtx, "acknowledge Discord tap", err)))
	}
	candidate, ok := adapter.candidateFromInteraction(adapter.rootCtx, event.Interaction)
	if !ok {
		return
	}
	adapter.enqueue(candidate)
}

// enqueue blocks while the queue is full: that is bounded backpressure, and
// an accepted message is never silently dropped.
func (adapter *Adapter) enqueue(candidate conversation.IncomingCandidate) {
	select {
	case <-adapter.rootCtx.Done():
	case adapter.queue <- candidate:
	}
}

func (adapter *Adapter) worker() {
	defer adapter.wait.Done()
	for {
		select {
		case <-adapter.rootCtx.Done():
			return
		case candidate := <-adapter.queue:
			if err := adapter.handler.Handle(adapter.rootCtx, candidate); err != nil {
				adapter.logger.Error("inbound processing failed", "code", agent.CodeOf(err), "error", err)
				continue
			}
			// The chat row exists now, so its name can be stored.
			if candidate.ChatKind == conversation.ChatGroup {
				if channel, err := adapter.client.State.Channel(candidate.ProviderChatAddress); err == nil {
					adapter.rememberChannelName(adapter.rootCtx, channel)
				}
			}
		}
	}
}

func (adapter *Adapter) emitConnection(event account.ConnectionEvent) {
	select {
	case adapter.events <- event:
	default:
	}
}

func (adapter *Adapter) emitFatal(err error) {
	select {
	case adapter.fatal <- err:
	default:
	}
	if adapter.cancel != nil {
		adapter.cancel()
	}
}

func (adapter *Adapter) removeHandlers() {
	for _, remove := range adapter.removers {
		remove()
	}
	adapter.removers = nil
}

func (adapter *Adapter) stopAfterFailedStart() {
	adapter.ready.Store(false)
	if adapter.cancel != nil {
		adapter.cancel()
	}
	adapter.removeHandlers()
	_ = adapter.client.Close()
	adapter.wait.Wait()
	adapter.started.Store(false)
}

func (adapter *Adapter) sendStripe(chatID string) *sync.Mutex {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(chatID))
	return &adapter.stripes[hash.Sum32()%sendStripeCount]
}
