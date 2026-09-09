package hypermeow

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mdp/qrterminal/v3"
	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/socket"
	"github.com/polymorfa/hypermeow/store/sqlstore"
	"github.com/polymorfa/hypermeow/types"
	"github.com/polymorfa/hypermeow/types/events"
	waLog "github.com/polymorfa/hypermeow/util/log"
	"google.golang.org/protobuf/proto"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/account"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/action"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

const sendStripeCount = 64

type CandidateHandler interface {
	Handle(context.Context, conversation.IncomingCandidate) error
}

type TargetStore interface {
	ResolveChatAddress(context.Context, agent.Key) (string, error)
	ReconcileAccountPolicy(context.Context, identity.TenantID, identity.AccountID, string, []string) error
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
	Logger          *slog.Logger
}

type Adapter struct {
	tenantID       identity.TenantID
	accountID      identity.AccountID
	owner          string
	allowlist      map[string]struct{}
	connectTimeout time.Duration
	sendTimeout    time.Duration
	pairing        PairingSink
	targets        TargetStore
	handler        CandidateHandler
	logger         *slog.Logger
	container      *sqlstore.Container
	client         *whatsmeow.Client
	queue          chan conversation.IncomingCandidate
	workers        uint32
	ready          atomic.Bool
	started        atomic.Bool
	closed         atomic.Bool
	events         chan account.ConnectionEvent
	fatal          chan error
	rootCtx        context.Context
	cancel         context.CancelFunc
	eventHandlerID uint32
	wait           sync.WaitGroup
	stripes        [sendStripeCount]sync.Mutex
}

func Open(ctx context.Context, config Config) (*Adapter, error) {
	if config.TenantID.IsZero() || config.AccountID.IsZero() || config.Targets == nil {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "open WhatsApp adapter", fmt.Errorf("identity and target resolver are required"))
	}
	if config.QueueCapacity == 0 || config.Workers == 0 || config.ConnectTimeout <= 0 || config.SendTimeout <= 0 {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "open WhatsApp adapter", fmt.Errorf("positive queue, worker, and timeout values are required"))
	}
	owner, err := normalizeAddress(config.OwnerAddress)
	if err != nil {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "normalize configured owner", err)
	}
	allowlist := make(map[string]struct{}, len(config.Allowlist))
	for _, raw := range config.Allowlist {
		normalized, err := normalizeAddress(raw)
		if err != nil {
			return nil, agent.NewError(agent.ErrorInvalidArgument, "normalize configured allowlist", err)
		}
		allowlist[normalized] = struct{}{}
	}
	if len(allowlist) == 0 {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "open WhatsApp adapter", fmt.Errorf("allowlist must fail closed"))
	}
	normalizedAllowlist := make([]string, 0, len(allowlist))
	for address := range allowlist {
		normalizedAllowlist = append(normalizedAllowlist, address)
	}
	if err := config.Targets.ReconcileAccountPolicy(ctx, config.TenantID, config.AccountID, owner, normalizedAllowlist); err != nil {
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
		owner:          owner,
		allowlist:      allowlist,
		connectTimeout: config.ConnectTimeout,
		sendTimeout:    config.SendTimeout,
		pairing:        config.Pairing,
		targets:        config.Targets,
		logger:         logger,
		container:      container,
		client:         client,
		queue:          make(chan conversation.IncomingCandidate, config.QueueCapacity),
		workers:        config.Workers,
		events:         make(chan account.ConnectionEvent, 8),
		fatal:          make(chan error, 1),
	}
	client.AutoReconnectHook = adapter.handleReconnectFailure
	return adapter, nil
}

func (adapter *Adapter) BindHandler(handler CandidateHandler) error {
	if handler == nil {
		return agent.NewError(agent.ErrorInvalidArgument, "bind WhatsApp handler", fmt.Errorf("candidate handler is required"))
	}
	if adapter.started.Load() || adapter.handler != nil {
		return agent.NewError(agent.ErrorConflict, "bind WhatsApp handler", fmt.Errorf("handler is already bound or adapter has started"))
	}
	adapter.handler = handler
	return nil
}

func (adapter *Adapter) Start(ctx context.Context) error {
	if adapter.closed.Load() {
		return agent.NewError(agent.ErrorNotReady, "start WhatsApp adapter", fmt.Errorf("adapter is closed"))
	}
	if adapter.handler == nil {
		return agent.NewError(agent.ErrorInvalidArgument, "start WhatsApp adapter", fmt.Errorf("candidate handler must be bound first"))
	}
	if !adapter.started.CompareAndSwap(false, true) {
		return agent.NewError(agent.ErrorConflict, "start WhatsApp adapter", fmt.Errorf("adapter is already started"))
	}
	if adapter.client.Store.ID == nil && adapter.pairing == nil {
		adapter.started.Store(false)
		return agent.NewError(agent.ErrorNotReady, "start WhatsApp adapter", fmt.Errorf("fresh device requires explicit pairing output"))
	}
	adapter.rootCtx, adapter.cancel = context.WithCancel(ctx)
	for index := uint32(0); index < adapter.workers; index++ {
		adapter.wait.Add(1)
		go adapter.worker()
	}
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
	if adapter.started.Swap(false) {
		adapter.ready.Store(false)
		if adapter.cancel != nil {
			adapter.cancel()
		}
		adapter.client.RemoveEventHandler(adapter.eventHandlerID)
		adapter.client.Disconnect()
		done := make(chan struct{})
		go func() {
			adapter.wait.Wait()
			close(done)
		}()
		select {
		case <-ctx.Done():
			return agent.NewError(agent.ErrorTimeout, "stop WhatsApp adapter", ctx.Err())
		case <-done:
		}
	}
	if adapter.closed.CompareAndSwap(false, true) {
		if err := adapter.container.Close(); err != nil {
			return agent.NewError(agent.ErrorStorageFailure, "close WhatsApp device store", err)
		}
	}
	return nil
}

func (adapter *Adapter) Ready() bool                            { return adapter.ready.Load() }
func (adapter *Adapter) Events() <-chan account.ConnectionEvent { return adapter.events }
func (adapter *Adapter) Fatal() <-chan error                    { return adapter.fatal }
func (adapter *Adapter) QueueUsage() (int, int)                 { return len(adapter.queue), cap(adapter.queue) }

func (adapter *Adapter) SendText(ctx context.Context, request action.SendTextRequest) (action.SendTextResult, error) {
	if !adapter.ready.Load() {
		return action.SendTextResult{}, agent.NewError(agent.ErrorNotReady, "send WhatsApp text", fmt.Errorf("account is not connected"))
	}
	address, err := adapter.targets.ResolveChatAddress(ctx, request.Key)
	if err != nil {
		return action.SendTextResult{}, err
	}
	target, err := types.ParseJID(address)
	if err != nil || target.IsEmpty() {
		return action.SendTextResult{}, agent.NewError(agent.ErrorIntegrityFailure, "resolve WhatsApp target", fmt.Errorf("stored target is invalid"))
	}
	stripe := adapter.sendStripe(request.Key.ChatID.String())
	stripe.Lock()
	defer stripe.Unlock()
	sendCtx, cancel := context.WithTimeout(ctx, adapter.sendTimeout)
	defer cancel()
	response, err := adapter.client.SendMessage(sendCtx, target.ToNonAD(), &waE2E.Message{Conversation: proto.String(request.Text)})
	if err != nil {
		if sendCtx.Err() == context.DeadlineExceeded {
			return action.SendTextResult{}, agent.NewError(agent.ErrorTimeout, "send WhatsApp text", sendCtx.Err())
		}
		if sendCtx.Err() == context.Canceled {
			return action.SendTextResult{}, agent.NewError(agent.ErrorCancelled, "send WhatsApp text", sendCtx.Err())
		}
		return action.SendTextResult{}, agent.NewError(agent.ErrorProviderFailure, "send WhatsApp text", fmt.Errorf("native send failed"))
	}
	return action.SendTextResult{ProviderReceipt: string(response.ID)}, nil
}

func (adapter *Adapter) handleEvent(event any) {
	switch typed := event.(type) {
	case *events.PairSuccess:
		adapter.logger.Info("WhatsApp pairing completed")
	case *events.Connected:
		adapter.ready.Store(true)
		adapter.logger.Info("WhatsApp account connected")
		adapter.emitConnection(account.ConnectionEvent{Connected: true, Code: "open"})
	case *events.Disconnected:
		adapter.ready.Store(false)
		adapter.logger.Warn("WhatsApp account disconnected; reconnecting")
		adapter.emitConnection(account.ConnectionEvent{Connected: false, Code: "reconnecting"})
	case *events.StreamError:
		adapter.logger.Warn("WhatsApp stream error", "reason", streamErrorReason(typed))
	case *events.KeepAliveTimeout:
		adapter.logger.Warn("WhatsApp keepalive timeout", "consecutive_failures", typed.ErrorCount)
	case events.PermanentDisconnect:
		adapter.ready.Store(false)
		adapter.emitFatal(agent.NewError(agent.ErrorUnavailable, "WhatsApp permanent disconnect", fmt.Errorf("%s", typed.PermanentDisconnectDescription())))
	case *events.Message:
		candidate, ok := adapter.normalizeMessage(typed)
		if !ok {
			adapter.logger.Info("inbound event ignored", "reason", ignoredNativeReason(typed))
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
	adapter.logger.Warn("WhatsApp reconnect attempt failed", "reason", reconnectFailureReason(err))
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

func ignoredNativeReason(event *events.Message) string {
	if event == nil || event.Message == nil {
		return "invalid_event"
	}
	if event.IsEdit {
		return "edited_message"
	}
	if event.Message.GetConversation() == "" && event.Message.GetExtendedTextMessage().GetText() == "" && event.Message.GetStickerMessage() == nil {
		return "unsupported_content"
	}
	return "invalid_metadata"
}

func (adapter *Adapter) normalizeMessage(event *events.Message) (conversation.IncomingCandidate, bool) {
	if event == nil || event.Message == nil || event.IsEdit {
		return conversation.IncomingCandidate{}, false
	}
	text := event.Message.GetConversation()
	extended := event.Message.GetExtendedTextMessage()
	if text == "" && extended != nil {
		text = extended.GetText()
	}
	contextInfo := (*waE2E.ContextInfo)(nil)
	if extended != nil {
		contextInfo = extended.GetContextInfo()
	}
	if text == "" {
		if sticker := event.Message.GetStickerMessage(); sticker != nil {
			// Part 2 remains text-only for the model, but the canonical transcript
			// must not lose visible group chronology when someone sends a sticker.
			text = "【sticker】"
			contextInfo = sticker.GetContextInfo()
		}
	}
	if text == "" {
		return conversation.IncomingCandidate{}, false
	}
	chat := event.Info.Chat.ToNonAD()
	sender := event.Info.Sender.ToNonAD()
	senderLID, ok := lidAddress(sender, event.Info.SenderAlt)
	if !ok {
		// Identity is fail-closed: never invent a senderRef from a phone alias.
		return conversation.IncomingCandidate{}, false
	}
	senderPhone := phoneAddress(sender, event.Info.SenderAlt)
	if !event.Info.IsGroup {
		if event.Info.IsFromMe {
			chat = phoneAddress(chat, event.Info.RecipientAlt)
		} else {
			chat = phoneAddress(chat, event.Info.SenderAlt)
		}
	}
	if chat.IsEmpty() || event.Info.ID == "" || event.Info.Timestamp.IsZero() {
		return conversation.IncomingCandidate{}, false
	}
	chatKind := conversation.ChatDirect
	if chat == types.StatusBroadcastJID {
		chatKind = conversation.ChatStatus
	} else if event.Info.IsGroup {
		chatKind = conversation.ChatGroup
	}
	chatAddress := chat.String()
	_, allowlisted := adapter.allowlist[chatAddress]
	if !allowlisted && !event.Info.IsGroup {
		for _, alternative := range []types.JID{event.Info.SenderAlt, event.Info.RecipientAlt} {
			if !alternative.IsEmpty() {
				if _, exists := adapter.allowlist[alternative.ToNonAD().String()]; exists {
					allowlisted = true
					break
				}
			}
		}
	}
	mentioned := false
	quotedMessageID := ""
	if contextInfo != nil {
		quotedMessageID = contextInfo.GetStanzaID()
	}
	if chatKind == conversation.ChatGroup && contextInfo != nil {
		mentioned = adapter.mentionsOwnAccount(contextInfo.GetMentionedJID())
	}
	return conversation.IncomingCandidate{
		TenantID:                adapter.tenantID,
		AccountID:               adapter.accountID,
		ProviderMessageID:       string(event.Info.ID),
		ProviderQuotedMessageID: quotedMessageID,
		ProviderChatAddress:     chatAddress,
		SenderLID:               mustLID(senderLID),
		ProviderSenderPhone:     jidString(senderPhone),
		SenderName:              event.Info.PushName,
		ChatKind:                chatKind,
		Text:                    text,
		MentionsBot:             mentioned,
		FromMe:                  event.Info.IsFromMe,
		Owner:                   adapter.isConfiguredOwner(sender, event.Info.SenderAlt),
		Allowlisted:             allowlisted,
		OccurredAt:              event.Info.Timestamp.UTC(),
		ReceivedAt:              time.Now().UTC(),
	}, true
}

func (adapter *Adapter) isConfiguredOwner(addresses ...types.JID) bool {
	for _, address := range addresses {
		if !address.IsEmpty() && address.ToNonAD().String() == adapter.owner {
			return true
		}
	}
	return false
}

func phoneAddress(primary types.JID, alternatives ...types.JID) types.JID {
	if primary.Server == types.DefaultUserServer || primary.Server == types.HostedServer {
		return primary.ToNonAD()
	}
	for _, alternative := range alternatives {
		alternative = alternative.ToNonAD()
		if alternative.Server == types.DefaultUserServer || alternative.Server == types.HostedServer {
			return alternative
		}
	}
	return primary.ToNonAD()
}

func lidAddress(addresses ...types.JID) (types.JID, bool) {
	for _, address := range addresses {
		address = address.ToNonAD()
		if (address.Server == types.HiddenUserServer || address.Server == types.HostedLIDServer) && address.User != "" {
			return address, true
		}
	}
	return types.EmptyJID, false
}

func mustLID(address types.JID) identity.LID {
	lid, err := identity.ParseLID(address.ToNonAD().String())
	if err != nil {
		panic("validated WhatsApp LID could not be parsed")
	}
	return lid
}

func jidString(address types.JID) string {
	if address.IsEmpty() {
		return ""
	}
	return address.ToNonAD().String()
}

func (adapter *Adapter) mentionsOwnAccount(mentioned []string) bool {
	if len(mentioned) == 0 {
		return false
	}
	own := make(map[string]struct{}, 2)
	if adapter.client.Store.ID != nil {
		own[adapter.client.Store.ID.ToNonAD().String()] = struct{}{}
	}
	if !adapter.client.Store.LID.IsEmpty() {
		own[adapter.client.Store.LID.ToNonAD().String()] = struct{}{}
	}
	for _, raw := range mentioned {
		if normalized, err := normalizeAddress(raw); err == nil {
			if _, exists := own[normalized]; exists {
				return true
			}
		}
	}
	return false
}

func (adapter *Adapter) worker() {
	defer adapter.wait.Done()
	for {
		select {
		case <-adapter.rootCtx.Done():
			return
		case candidate := <-adapter.queue:
			if err := adapter.handler.Handle(adapter.rootCtx, candidate); err != nil {
				adapter.logger.Error("inbound processing failed", "code", agent.CodeOf(err))
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

// TerminalPairingSink intentionally bypasses structured logging. It should be
// constructed only when terminal pairing output is enabled.
type TerminalPairingSink struct {
	Writer io.Writer
	mu     sync.Mutex
}

func (sink *TerminalPairingSink) ShowPairingCode(code string, validFor time.Duration) error {
	if sink == nil || sink.Writer == nil {
		return errors.New("pairing output is unavailable")
	}
	if code == "" || len(code) > 2048 || validFor <= 0 {
		return errors.New("pairing payload or lifetime is invalid")
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	checked := &errorTrackingWriter{writer: sink.Writer}
	_, _ = fmt.Fprintf(checked, "\nWhatsApp pairing QR (sensitive, valid for about %s):\n", validFor.Round(time.Second))
	qrterminal.GenerateHalfBlock(code, qrterminal.L, checked)
	_, _ = fmt.Fprintln(checked, "Scan from WhatsApp > Linked devices. Do not share this QR.")
	return checked.err
}

type errorTrackingWriter struct {
	writer io.Writer
	err    error
}

func (writer *errorTrackingWriter) Write(payload []byte) (int, error) {
	if writer.err != nil {
		return 0, writer.err
	}
	written, err := writer.writer.Write(payload)
	if err != nil {
		writer.err = err
	}
	return written, err
}
