package hypermeow

import (
	"context"
	"encoding/base64"
	"errors"
	"runtime"
	"sync"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/config"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/control"
	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/store/sqlstore"
	"github.com/polymorfa/hypermeow/types/events"
	waLog "github.com/polymorfa/hypermeow/util/log"
	"rsc.io/qr"
)

const phonePairingCodeLifetime = 160 * time.Second

// SessionFactory creates the session-only WhatsApp client used by the Wails
// application. This path deliberately has no message queue, handler, or send
// interface.
type SessionFactory struct{}

func NewSessionFactory() *SessionFactory { return &SessionFactory{} }

func (factory *SessionFactory) OpenSession(ctx context.Context, snapshot config.Snapshot) (control.ManagedSession, error) {
	if factory == nil || snapshot.WhatsAppDatabasePath() == "" || snapshot.ConnectTimeout() <= 0 {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "open WhatsApp session", errors.New("session database path and connection timeout are required"))
	}
	container, err := openDeviceStore(ctx, snapshot.WhatsAppDatabasePath(), waLog.Noop)
	if err != nil {
		return nil, agent.NewError(agent.ErrorStorageFailure, "open WhatsApp session store", errors.New("WhatsApp device store could not be opened"))
	}
	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		_ = container.Close()
		return nil, agent.NewError(agent.ErrorStorageFailure, "load WhatsApp session", errors.New("WhatsApp device record could not be loaded"))
	}
	client := whatsmeow.NewClient(device, waLog.Noop)
	client.EnableAutoReconnect = true
	runtime := &SessionRuntime{container: container, client: client, connectTimeout: snapshot.ConnectTimeout(), events: make(chan any, 32), connected: make(chan struct{}, 1), reconnect: make(chan struct{}, 1)}
	runtime.eventHandlerID = client.AddEventHandler(runtime.handleEvent)
	return runtime, nil
}

// SessionRuntime exposes only connection/session operations. It has no method
// capable of accepting inbound messages or sending WhatsApp messages.
type SessionRuntime struct {
	container      *sqlstore.Container
	client         *whatsmeow.Client
	connectTimeout time.Duration
	events         chan any
	connected      chan struct{}
	reconnect      chan struct{}
	eventHandlerID uint32

	mu            sync.Mutex
	running       bool
	closed        bool
	closeErr      error
	sessionCtx    context.Context
	sessionCancel context.CancelFunc
}

func (runtime *SessionRuntime) HasSession() bool {
	return runtime != nil && runtime.client != nil && runtime.client.Store != nil && runtime.client.Store.ID != nil
}

func (runtime *SessionRuntime) WhatsAppAccountID() string {
	if !runtime.HasSession() {
		return ""
	}
	return runtime.client.Store.ID.ToNonAD().String()
}

func (runtime *SessionRuntime) Run(ctx context.Context, request control.SessionRunRequest, emit func(control.SessionRuntimeEvent)) error {
	if runtime == nil || runtime.client == nil || emit == nil {
		return agent.NewError(agent.ErrorInvalidArgument, "run WhatsApp session", errors.New("session and event callback are required"))
	}
	runtime.mu.Lock()
	if runtime.closed {
		runtime.mu.Unlock()
		return agent.NewError(agent.ErrorNotReady, "run WhatsApp session", errors.New("session runtime is closed"))
	}
	if runtime.running {
		runtime.mu.Unlock()
		return agent.NewError(agent.ErrorConflict, "run WhatsApp session", errors.New("session runtime is already running"))
	}
	runtime.running = true
	runtime.mu.Unlock()
	defer func() {
		runtime.mu.Lock()
		runtime.running = false
		runtime.mu.Unlock()
	}()

	var qrEvents <-chan whatsmeow.QRChannelItem
	if request.Mode == control.SessionRunPairing {
		if runtime.HasSession() {
			return agent.NewError(agent.ErrorConflict, "prepare WhatsApp pairing", errors.New("a linked device already exists"))
		}
		var err error
		qrEvents, err = runtime.client.GetQRChannel(ctx)
		if err != nil {
			return agent.NewError(agent.ErrorProviderFailure, "prepare WhatsApp pairing", errors.New("WhatsApp pairing could not be prepared"))
		}
	} else if !runtime.HasSession() {
		return agent.NewError(agent.ErrorIntegrityFailure, "resume WhatsApp session", errors.New("saved WhatsApp device is missing"))
	}

	if request.Mode == control.SessionRunPairing {
		emit(control.SessionRuntimeEvent{State: control.RuntimePairing})
	} else {
		emit(control.SessionRuntimeEvent{State: control.RuntimeConnecting})
	}
	if err := runtime.connect(ctx); err != nil {
		return err
	}
	phoneCodeRequested := false
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-runtime.reconnect:
			emit(control.SessionRuntimeEvent{State: control.RuntimeReconnecting})
			if runtime.client.IsConnected() {
				runtime.client.ResetConnection()
			} else if err := runtime.connect(ctx); err != nil {
				return err
			}
		case rawEvent := <-runtime.events:
			switch event := rawEvent.(type) {
			case *events.Connected:
				if runtime.client.IsLoggedIn() {
					emit(control.SessionRuntimeEvent{State: control.RuntimeConnected, WhatsAppAccountID: runtime.WhatsAppAccountID()})
				}
			case *events.Disconnected:
				if runtime.HasSession() {
					emit(control.SessionRuntimeEvent{State: control.RuntimeReconnecting})
				}
			case *events.LoggedOut:
				emit(control.SessionRuntimeEvent{State: control.RuntimeRevoked})
				return nil
			case *events.ConnectFailure:
				if event.Reason.IsLoggedOut() {
					emit(control.SessionRuntimeEvent{State: control.RuntimeRevoked})
					return nil
				}
				return agent.NewError(agent.ErrorUnavailable, "connect WhatsApp session", errors.New("WhatsApp ended the session connection"))
			case events.PermanentDisconnect:
				return agent.NewError(agent.ErrorUnavailable, "connect WhatsApp session", errors.New("WhatsApp ended the session connection"))
			}
		case item, open := <-qrEvents:
			if !open {
				qrEvents = nil
				continue
			}
			switch item.Event {
			case whatsmeow.QRChannelEventCode:
				if request.Method == control.PairingPhoneCode && !phoneCodeRequested {
					phoneCodeRequested = true
					code, err := runtime.client.PairPhone(ctx, request.Phone, true, whatsmeow.PairClientChrome, pairingClientDisplayName())
					if err != nil {
						return agent.NewError(agent.ErrorProviderFailure, "request WhatsApp phone pairing code", errors.New("WhatsApp phone pairing code could not be requested"))
					}
					emit(control.SessionRuntimeEvent{State: control.RuntimePairing, Pairing: &control.SessionPairing{Method: control.PairingPhoneCode, Code: code, Generation: 1, ExpiresAt: time.Now().Add(phonePairingCodeLifetime)}})
				} else if request.Method == control.PairingQR {
					dataURL, err := encodeQRDataURL(item.Code)
					if err != nil {
						return agent.NewError(agent.ErrorInternal, "render WhatsApp pairing QR", errors.New("WhatsApp QR could not be rendered"))
					}
					emit(control.SessionRuntimeEvent{State: control.RuntimePairing, Pairing: &control.SessionPairing{Method: control.PairingQR, QRCodeDataURL: dataURL, ExpiresAt: time.Now().Add(item.Timeout)}})
				}
			case "success":
				emit(control.SessionRuntimeEvent{State: control.RuntimeConnecting})
			case "timeout":
				return agent.NewError(agent.ErrorTimeout, "pair WhatsApp device", errors.New("WhatsApp pairing window expired"))
			case "error", "passkey-request", "passkey-confirmation", "err-client-outdated", "err-scanned-without-multidevice", "err-unexpected-state":
				return agent.NewError(agent.ErrorProviderFailure, "pair WhatsApp device", errors.New("WhatsApp pairing did not complete"))
			}
		}
	}
}

func (runtime *SessionRuntime) Logout(ctx context.Context) error {
	if !runtime.HasSession() {
		return agent.NewError(agent.ErrorNotReady, "logout WhatsApp session", errors.New("no linked WhatsApp device is available"))
	}
	if !runtime.client.IsLoggedIn() || !runtime.client.IsConnected() {
		if err := runtime.connect(ctx); err != nil {
			return err
		}
		select {
		case <-runtime.connected:
		case <-ctx.Done():
			return agent.NewError(agent.ErrorTimeout, "connect before WhatsApp logout", ctx.Err())
		case <-time.After(runtime.connectTimeout):
			return agent.NewError(agent.ErrorTimeout, "connect before WhatsApp logout", errors.New("connection timed out"))
		}
	}
	if err := runtime.client.Logout(ctx); err != nil {
		return agent.NewError(agent.ErrorProviderFailure, "logout WhatsApp session", errors.New("WhatsApp did not confirm logout"))
	}
	return nil
}

func (runtime *SessionRuntime) Reconnect() error {
	if runtime == nil {
		return agent.NewError(agent.ErrorNotReady, "reconnect WhatsApp session", errors.New("session runtime is not available"))
	}
	runtime.mu.Lock()
	running := runtime.running && !runtime.closed
	runtime.mu.Unlock()
	if !running {
		return agent.NewError(agent.ErrorNotReady, "reconnect WhatsApp session", errors.New("session runtime is not running"))
	}
	select {
	case runtime.reconnect <- struct{}{}:
		return nil
	default:
		return nil
	}
}

func (runtime *SessionRuntime) Close(context.Context) error {
	if runtime == nil {
		return nil
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.closed {
		return runtime.closeErr
	}
	runtime.closed = true
	if runtime.sessionCancel != nil {
		runtime.sessionCancel()
	}
	runtime.client.Disconnect()
	runtime.client.RemoveEventHandler(runtime.eventHandlerID)
	if err := runtime.container.Close(); err != nil {
		runtime.closeErr = agent.NewError(agent.ErrorStorageFailure, "close WhatsApp session store", errors.New("WhatsApp device store could not be closed"))
	}
	return runtime.closeErr
}

func (runtime *SessionRuntime) connect(ctx context.Context) error {
	runtime.mu.Lock()
	if runtime.closed {
		runtime.mu.Unlock()
		return agent.NewError(agent.ErrorNotReady, "connect WhatsApp session", errors.New("session runtime is closed"))
	}
	if runtime.sessionCtx == nil {
		runtime.sessionCtx, runtime.sessionCancel = context.WithCancel(ctx)
	}
	connectCtx, cancelSession := runtime.sessionCtx, runtime.sessionCancel
	runtime.mu.Unlock()

	connectResult := make(chan error, 1)
	go func() { connectResult <- runtime.client.ConnectContext(connectCtx) }()
	timer := time.NewTimer(runtime.connectTimeout)
	defer timer.Stop()
	select {
	case err := <-connectResult:
		if err == nil {
			return nil
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return agent.NewError(agent.ErrorCancelled, "connect WhatsApp session", ctx.Err())
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return agent.NewError(agent.ErrorTimeout, "connect WhatsApp session", ctx.Err())
		}
		return agent.NewError(agent.ErrorUnavailable, "connect WhatsApp session", errors.New("WhatsApp could not connect"))
	case <-timer.C:
		cancelSession()
		<-connectResult
		return agent.NewError(agent.ErrorTimeout, "connect WhatsApp session", context.DeadlineExceeded)
	case <-ctx.Done():
		cancelSession()
		<-connectResult
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return agent.NewError(agent.ErrorTimeout, "connect WhatsApp session", ctx.Err())
		}
		return agent.NewError(agent.ErrorCancelled, "connect WhatsApp session", ctx.Err())
	}
}

func (runtime *SessionRuntime) handleEvent(event any) {
	if _, isMessage := event.(*events.Message); isMessage {
		return
	}
	select {
	case runtime.events <- event:
	default:
	}
	if _, ok := event.(*events.Connected); ok {
		select {
		case runtime.connected <- struct{}{}:
		default:
		}
	}
}

func encodeQRDataURL(value string) (string, error) {
	if value == "" {
		return "", errors.New("QR payload is empty")
	}
	code, err := qr.Encode(value, qr.M)
	if err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(code.PNG()), nil
}

func pairingClientDisplayName() string {
	switch runtime.GOOS {
	case "windows":
		return "Chrome (Windows)"
	case "darwin":
		return "Chrome (macOS)"
	case "android":
		return "Chrome (Android)"
	default:
		return "Chrome (Linux)"
	}
}

var _ control.SessionRuntimeFactory = (*SessionFactory)(nil)
var _ control.ManagedSession = (*SessionRuntime)(nil)
