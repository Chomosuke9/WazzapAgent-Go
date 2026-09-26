package hypermeow

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/socket"
	"github.com/polymorfa/hypermeow/store/sqlstore"
	"github.com/polymorfa/hypermeow/types"
	"github.com/polymorfa/hypermeow/types/events"
	waLog "github.com/polymorfa/hypermeow/util/log"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/account"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/action"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	broadcastmodel "github.com/Chomosuke9/WazzapAgent-Go/internal/broadcast"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/policy"
)

const sendStripeCount = 64

type CandidateHandler interface {
	Handle(context.Context, conversation.IncomingCandidate) error
}

type TargetStore interface {
	ResolveChatAddress(context.Context, agent.Key) (string, error)
	ResolveMessageTarget(context.Context, agent.Key, identity.MessageID) (chatAddress, providerMessageID, senderAddress string, occurredAt time.Time, err error)
	ReconcileAccountPolicy(context.Context, identity.TenantID, identity.AccountID, string, []string) error
	ResolveLID(context.Context, agent.Key, identity.SenderRef) (identity.LID, error)
	SetChatMute(context.Context, agent.Key, identity.SenderRef, uint32, time.Time) error
}

type GroupNameStore interface {
	SaveGroupName(context.Context, identity.TenantID, identity.AccountID, string, string) error
}

type GroupMetadataStore interface {
	InvalidateGroupMetadata(context.Context, identity.TenantID, identity.AccountID) error
	ReplaceGroupMetadata(context.Context, identity.TenantID, identity.AccountID, map[string][]byte, int64) error
	LoadGroupMetadata(context.Context, identity.TenantID, identity.AccountID, string) ([]byte, int64, error)
	UpsertGroupMetadata(context.Context, identity.TenantID, identity.AccountID, string, []byte, int64) error
	DeleteGroupMetadata(context.Context, identity.TenantID, identity.AccountID, string) error
}

type PairingSink interface {
	ShowPairingCode(string, time.Duration) error
}

type Config struct {
	TenantID        identity.TenantID
	AccountID       identity.AccountID
	DeviceStorePath string
	OwnerAddress    string
	Allowlist       []string
	QueueCapacity   uint32
	Workers         uint32
	ConnectTimeout  time.Duration
	SendTimeout     time.Duration
	Pairing         PairingSink
	Targets         TargetStore
	GroupNames      GroupNameStore
	GroupMetadata   GroupMetadataStore
	Broadcasts      broadcastmodel.Store
	Logger          *slog.Logger
}

type Adapter struct {
	tenantID        identity.TenantID
	accountID       identity.AccountID
	normalizer      messageNormalizer
	connectTimeout  time.Duration
	sendTimeout     time.Duration
	pairing         PairingSink
	targets         TargetStore
	groupNames      GroupNameStore
	groupMetadata   GroupMetadataStore
	broadcasts      broadcastmodel.Store
	handler         CandidateHandler
	logger          *slog.Logger
	container       *sqlstore.Container
	client          *whatsmeow.Client
	queue           chan conversation.IncomingCandidate
	groupSync       chan struct{}
	groupMu         sync.RWMutex
	groups          map[types.JID]cachedGroup
	groupRevision   uint64
	groupsReady     bool
	workers         uint32
	memberHandlesMu sync.Mutex
	memberHandles   map[string]memberHandleSet
	broadcastMu     sync.Mutex
	broadcastSet    broadcastGroupHandleSet
	ready           atomic.Bool
	started         atomic.Bool
	closed          atomic.Bool
	events          chan account.ConnectionEvent
	fatal           chan error
	rootCtx         context.Context
	cancel          context.CancelFunc
	eventHandlerID  uint32
	wait            sync.WaitGroup
	stopOnce        sync.Once
	stopDone        chan struct{}
	stopErr         error
	stripes         [sendStripeCount]sync.Mutex
}

func Open(ctx context.Context, config Config) (*Adapter, error) {
	if config.TenantID.IsZero() || config.AccountID.IsZero() || config.Targets == nil || config.GroupMetadata == nil {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "open WhatsApp adapter", errors.New("identity, target resolver, and group metadata store are required"))
	}
	if config.QueueCapacity == 0 || config.Workers == 0 || config.ConnectTimeout <= 0 || config.SendTimeout <= 0 {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "open WhatsApp adapter", errors.New("positive queue, worker, and timeout values are required"))
	}
	owner, err := normalizeAddress(config.OwnerAddress)
	if err != nil {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "normalize configured owner", err)
	}
	allowlist := make([]string, 0, len(config.Allowlist))
	for _, raw := range config.Allowlist {
		normalized, err := normalizeAllowlistAddress(raw)
		if err != nil {
			return nil, agent.NewError(agent.ErrorInvalidArgument, "normalize configured allowlist", err)
		}
		allowlist = append(allowlist, normalized)
	}
	gate, err := policy.NewInboundGate(owner, allowlist)
	if err != nil {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "open WhatsApp adapter", err)
	}
	if err := config.Targets.ReconcileAccountPolicy(ctx, config.TenantID, config.AccountID, owner, gate.Allowlist()); err != nil {
		return nil, err
	}
	container, err := openDeviceStore(ctx, config.DeviceStorePath, waLog.Noop)
	if err != nil {
		return nil, err
	}
	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		_ = container.Close()
		return nil, agent.NewError(agent.ErrorStorageFailure, "load WhatsApp device", err)
	}
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	client := whatsmeow.NewClient(device, waLog.Noop)
	adapter := &Adapter{
		tenantID:       config.TenantID,
		accountID:      config.AccountID,
		connectTimeout: config.ConnectTimeout,
		sendTimeout:    config.SendTimeout,
		pairing:        config.Pairing,
		targets:        config.Targets,
		groupNames:     config.GroupNames,
		groupMetadata:  config.GroupMetadata,
		broadcasts:     config.Broadcasts,
		logger:         logger,
		container:      container,
		client:         client,
		queue:          make(chan conversation.IncomingCandidate, config.QueueCapacity),
		groupSync:      make(chan struct{}, 1),
		groups:         make(map[types.JID]cachedGroup),
		workers:        config.Workers,
		memberHandles:  make(map[string]memberHandleSet),
		events:         make(chan account.ConnectionEvent, 8),
		fatal:          make(chan error, 1),
	}
	adapter.normalizer = messageNormalizer{
		tenantID: config.TenantID, accountID: config.AccountID, gate: gate, client: client, groupRoles: adapter.groupRoleFlags,
	}
	client.AutoReconnectHook = adapter.handleReconnectFailure
	return adapter, nil
}

func (adapter *Adapter) BindHandler(handler CandidateHandler) error {
	if handler == nil {
		return agent.NewError(agent.ErrorInvalidArgument, "bind WhatsApp handler", errors.New("candidate handler is required"))
	}
	if adapter.started.Load() || adapter.handler != nil {
		return agent.NewError(agent.ErrorConflict, "bind WhatsApp handler", errors.New("handler is already bound or adapter has started"))
	}
	adapter.handler = handler
	return nil
}

func (adapter *Adapter) Start(ctx context.Context) error {
	if adapter.closed.Load() {
		return agent.NewError(agent.ErrorNotReady, "start WhatsApp adapter", errors.New("adapter is closed"))
	}
	if adapter.handler == nil {
		return agent.NewError(agent.ErrorInvalidArgument, "start WhatsApp adapter", errors.New("candidate handler must be bound first"))
	}
	if !adapter.started.CompareAndSwap(false, true) {
		return agent.NewError(agent.ErrorConflict, "start WhatsApp adapter", errors.New("adapter is already started"))
	}
	if adapter.client.Store.ID == nil && adapter.pairing == nil {
		adapter.started.Store(false)
		return agent.NewError(agent.ErrorNotReady, "start WhatsApp adapter", errors.New("fresh device requires explicit pairing output"))
	}
	adapter.rootCtx, adapter.cancel = context.WithCancel(ctx)
	for index := uint32(0); index < adapter.workers; index++ {
		adapter.wait.Add(1)
		go adapter.worker()
	}
	adapter.wait.Add(1)
	go adapter.groupNameSyncWorker()
	adapter.eventHandlerID = adapter.client.AddEventHandler(adapter.handleEvent)

	var qrChannel <-chan whatsmeow.QRChannelItem
	if adapter.client.Store.ID == nil {
		var err error
		qrChannel, err = adapter.client.GetQRChannel(adapter.rootCtx)
		if err != nil {
			adapter.stopAfterFailedStart()
			return agent.NewError(agent.ErrorProviderFailure, "prepare WhatsApp pairing", err)
		}
		adapter.wait.Add(1)
		go adapter.consumePairing(qrChannel)
	}
	if err := waitForInitialConnection(
		adapter.rootCtx,
		adapter.connectTimeout,
		adapter.client.ConnectContext,
		func() bool { return adapter.client.IsConnected() && adapter.client.IsLoggedIn() },
		adapter.events,
		adapter.fatal,
	); err != nil {
		adapter.stopAfterFailedStart()
		return err
	}
	adapter.ready.Store(adapter.client.IsConnected() && adapter.client.IsLoggedIn())
	if adapter.broadcasts != nil {
		adapter.wait.Add(1)
		go adapter.broadcastScheduleWorker()
	}
	return nil
}

func waitForInitialConnection(
	ctx context.Context,
	timeout time.Duration,
	connect func(context.Context) error,
	ready func() bool,
	eventsChannel <-chan account.ConnectionEvent,
	fatalChannel <-chan error,
) error {
	connectResult := make(chan error, 1)
	go func() { connectResult <- connect(ctx) }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	connectionObserved := false

	for {
		select {
		case err := <-connectResult:
			connectResult = nil
			if err != nil {
				if ctx.Err() != nil {
					return agent.NewError(agent.ErrorCancelled, "connect WhatsApp account", ctx.Err())
				}
				return agent.NewError(agent.ErrorUnavailable, "connect WhatsApp account", err)
			}
			if connectionObserved || ready() {
				return nil
			}
		case event, open := <-eventsChannel:
			if !open {
				eventsChannel = nil
				continue
			}
			if event.Connected {
				connectionObserved = true
				if connectResult == nil {
					return nil
				}
			}
		case err, open := <-fatalChannel:
			if !open {
				fatalChannel = nil
				continue
			}
			if err != nil {
				return err
			}
		case <-timer.C:
			return agent.NewError(agent.ErrorTimeout, "wait for WhatsApp connection", context.DeadlineExceeded)
		case <-ctx.Done():
			select {
			case err := <-fatalChannel:
				if err != nil {
					return err
				}
			default:
			}
			return agent.NewError(agent.ErrorCancelled, "wait for WhatsApp connection", ctx.Err())
		}
	}
}

func (adapter *Adapter) Stop(ctx context.Context) error {
	// One shutdown owns the workers and device store even if a caller times out.
	// Account runtime calls Stop only after Start has returned.
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
				adapter.client.RemoveEventHandler(adapter.eventHandlerID)
				adapter.client.Disconnect()
			}
			adapter.wait.Wait()
			if err := adapter.container.Close(); err != nil {
				adapter.stopErr = agent.NewError(agent.ErrorStorageFailure, "close WhatsApp device store", err)
			}
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
		return agent.NewError(agent.ErrorTimeout, "stop WhatsApp adapter", ctx.Err())
	}
}

func (adapter *Adapter) Ready() bool                            { return !adapter.closed.Load() && adapter.ready.Load() }
func (adapter *Adapter) Events() <-chan account.ConnectionEvent { return adapter.events }
func (adapter *Adapter) Fatal() <-chan error                    { return adapter.fatal }
func (adapter *Adapter) QueueUsage() (int, int)                 { return len(adapter.queue), cap(adapter.queue) }

func (adapter *Adapter) SendText(ctx context.Context, request action.SendTextRequest) (action.SendTextResult, error) {
	return adapter.send(ctx, request.Key, "send WhatsApp text", func(sendCtx context.Context, address string, target types.JID) (*waE2E.Message, error) {
		return adapter.textMessage(sendCtx, request, address, target)
	})
}

// SendButtons sends text with quick-reply buttons. A tap comes back as an
// ordinary inbound message whose text is the tapped button's ID.
func (adapter *Adapter) SendButtons(ctx context.Context, request action.SendButtonsRequest) (action.SendTextResult, error) {
	message, err := buttonsMessage(request)
	if err != nil {
		return action.SendTextResult{}, err
	}
	return adapter.send(ctx, request.Key, "send WhatsApp buttons", func(context.Context, string, types.JID) (*waE2E.Message, error) {
		return message, nil
	})
}

func (adapter *Adapter) send(ctx context.Context, key agent.Key, operation string, build func(context.Context, string, types.JID) (*waE2E.Message, error)) (action.SendTextResult, error) {
	if !adapter.Ready() {
		return action.SendTextResult{}, agent.NewError(agent.ErrorNotReady, operation, errors.New("account is not connected"))
	}
	address, err := adapter.targets.ResolveChatAddress(ctx, key)
	if err != nil {
		return action.SendTextResult{}, err
	}
	target, err := types.ParseJID(address)
	if err != nil || target.IsEmpty() {
		return action.SendTextResult{}, agent.NewError(agent.ErrorIntegrityFailure, "resolve WhatsApp target", errors.New("stored target is invalid"))
	}
	stripe := adapter.sendStripe(key.ChatID.String())
	stripe.Lock()
	defer stripe.Unlock()
	sendCtx, cancel := context.WithTimeout(ctx, adapter.sendTimeout)
	defer cancel()

	message, err := build(sendCtx, address, target)
	if err != nil {
		return action.SendTextResult{}, err
	}
	response, err := adapter.client.SendMessage(sendCtx, target.ToNonAD(), message)
	if err != nil {
		if sendCtx.Err() == context.DeadlineExceeded {
			return action.SendTextResult{}, agent.NewError(agent.ErrorTimeout, operation, sendCtx.Err())
		}
		if sendCtx.Err() == context.Canceled {
			return action.SendTextResult{}, agent.NewError(agent.ErrorCancelled, operation, sendCtx.Err())
		}
		return action.SendTextResult{}, agent.NewError(agent.ErrorProviderFailure, operation, err)
	}
	return action.SendTextResult{ProviderReceipt: string(response.ID)}, nil
}

func (adapter *Adapter) handleEvent(event any) {
	switch typed := event.(type) {
	case *events.PairSuccess:
		adapter.logger.Info("WhatsApp pairing completed")
	case *events.PairError:
		adapter.logger.Error("WhatsApp pairing failed", "error", typed.Error)
	case *events.PairPasskeyError:
		adapter.logger.Error("WhatsApp passkey pairing failed", "error", typed.Error, "continuation", typed.Continuation)
	case *events.Connected:
		adapter.ready.Store(true)
		adapter.clearGroupCache()
		adapter.logger.Info("WhatsApp account connected")
		adapter.emitConnection(account.ConnectionEvent{Connected: true, Code: "open"})
		adapter.requestGroupNameSync()
	case *events.Disconnected:
		adapter.ready.Store(false)
		adapter.clearGroupCache()
		adapter.logger.Warn("WhatsApp account disconnected; reconnecting")
		adapter.emitConnection(account.ConnectionEvent{Connected: false, Code: "reconnecting"})
	case *events.StreamError:
		adapter.logger.Warn("WhatsApp stream error", "reason", streamErrorReason(typed), "code", typed.Code, "raw", typed.Raw)
	case *events.CATRefreshError:
		adapter.logger.Error("WhatsApp CAT refresh failed", "error", typed.Error)
	case *events.KeepAliveTimeout:
		adapter.logger.Warn("WhatsApp keepalive timeout", "consecutive_failures", typed.ErrorCount)
	case events.PermanentDisconnect:
		adapter.ready.Store(false)
		adapter.clearGroupCache()
		adapter.emitFatal(agent.NewError(agent.ErrorUnavailable, "WhatsApp permanent disconnect", errors.New(typed.PermanentDisconnectDescription())))
	case *events.JoinedGroup:
		adapter.cacheJoinedGroup(typed.GroupInfo)
		adapter.cacheGroupName(adapter.rootCtx, typed.JID, typed.Name)
	case *events.GroupInfo:
		if adapter.applyGroupChange(typed) {
			adapter.requestGroupNameSync()
		}
		if typed.Name != nil {
			adapter.cacheGroupName(adapter.rootCtx, typed.JID, typed.Name.Name)
		}
	case *events.Message:
		candidate, ok := adapter.normalizer.normalizeMessage(adapter.rootCtx, typed)
		if !ok {
			adapter.logger.Debug("inbound event ignored", "reason", ignoredNativeReason(typed))
			return
		}
		// Blocking here is intentional bounded backpressure. Accepted native text
		// events are never silently dropped when the worker queue is full.
		select {
		case <-adapter.rootCtx.Done():
		case adapter.queue <- candidate:
		}
	}
}

func (adapter *Adapter) handleReconnectFailure(err error) bool {
	adapter.logger.Warn("WhatsApp reconnect attempt failed", "error", err, "reason", reconnectFailureReason(err))
	return adapter.rootCtx == nil || adapter.rootCtx.Err() == nil
}

func reconnectFailureReason(err error) string {
	if err == nil {
		return "unknown"
	}
	var statusError socket.ErrWithStatusCode
	if errors.As(err, &statusError) {
		return fmt.Sprintf("http_%d", statusError.StatusCode)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return "network_timeout"
	}
	normalized := strings.ToLower(err.Error())
	switch {
	case strings.Contains(normalized, "no such host"):
		return "dns_failure"
	case strings.Contains(normalized, "connection refused"):
		return "connection_refused"
	case strings.Contains(normalized, "noise handshake"):
		return "noise_handshake_failure"
	case errors.Is(err, socket.ErrDialFailed):
		return "websocket_dial_failure"
	default:
		return "provider_failure"
	}
}

func streamErrorReason(event *events.StreamError) string {
	if event == nil {
		return "unknown"
	}
	if event.Code != "" && len(event.Code) <= 8 {
		for _, character := range event.Code {
			if character < '0' || character > '9' {
				return "unknown"
			}
		}
		return "code_" + event.Code
	}
	if event.Raw != nil {
		if _, found := event.Raw.GetOptionalChildByTag("ping"); found {
			return "ping"
		}
	}
	return "unknown"
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
			}
		}
	}
}

func (adapter *Adapter) consumePairing(channel <-chan whatsmeow.QRChannelItem) {
	defer adapter.wait.Done()
	for {
		select {
		case <-adapter.rootCtx.Done():
			return
		case item, open := <-channel:
			if !open {
				return
			}
			switch item.Event {
			case whatsmeow.QRChannelEventCode:
				if err := adapter.pairing.ShowPairingCode(item.Code, item.Timeout); err != nil {
					adapter.emitFatal(agent.NewError(agent.ErrorProviderFailure, "show WhatsApp pairing code", err))
					return
				}
			case whatsmeow.QRChannelEventError:
				adapter.emitFatal(agent.NewError(agent.ErrorProviderFailure, "pair WhatsApp account", item.Error))
				return
			case whatsmeow.QRChannelSuccess.Event:
				return
			case whatsmeow.QRChannelTimeout.Event, whatsmeow.QRChannelClientOutdated.Event,
				whatsmeow.QRChannelScannedWithoutMultidevice.Event, whatsmeow.QRChannelErrUnexpectedEvent.Event:
				adapter.emitFatal(agent.NewError(agent.ErrorProviderFailure, "pair WhatsApp account", fmt.Errorf("pairing ended with %s", item.Event)))
				return
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

func (adapter *Adapter) stopAfterFailedStart() {
	adapter.ready.Store(false)
	if adapter.cancel != nil {
		adapter.cancel()
	}
	adapter.client.RemoveEventHandler(adapter.eventHandlerID)
	adapter.client.Disconnect()
	adapter.wait.Wait()
	adapter.started.Store(false)
}

func (adapter *Adapter) sendStripe(chatID string) *sync.Mutex {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(chatID))
	return &adapter.stripes[hash.Sum32()%sendStripeCount]
}

func normalizeAddress(raw string) (string, error) {
	jid, err := types.ParseJID(raw)
	if err != nil || jid.IsEmpty() || jid.User == "" {
		return "", fmt.Errorf("invalid provider address")
	}
	return jid.ToNonAD().String(), nil
}

func normalizeAllowlistAddress(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if policy.IsChatAllowlistWildcard(value) {
		return value, nil
	}
	return normalizeAddress(value)
}
