package discord

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/Chomosuke9/DiscordAgent-Go/internal/agent"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/config"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/control"
)

// SessionFactory opens the session-only Discord client the app uses to link
// a bot and show it online. It has no inbound handler and cannot send.
type SessionFactory struct {
	Logger *slog.Logger
}

func NewSessionFactory() *SessionFactory { return &SessionFactory{} }

func (factory *SessionFactory) OpenSession(_ context.Context, snapshot config.Snapshot) (control.ManagedSession, error) {
	if factory == nil || snapshot.DiscordTokenPath() == "" || snapshot.ConnectTimeout() <= 0 {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "open Discord session", errors.New("token path and connection timeout are required"))
	}
	token, err := ReadToken(snapshot.DiscordTokenPath())
	if err != nil {
		return nil, agent.NewError(agent.ErrorStorageFailure, "open Discord session", err)
	}
	logger := factory.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &SessionRuntime{
		tokenPath: snapshot.DiscordTokenPath(), token: token, botID: control.BotIDFromToken(token),
		connectTimeout: snapshot.ConnectTimeout(), logger: logger, reconnect: make(chan struct{}, 1),
	}, nil
}

// SessionRuntime links a bot token and keeps the bot connected while no
// Agent runs. It exposes no way to read or send messages.
type SessionRuntime struct {
	tokenPath      string
	connectTimeout time.Duration
	logger         *slog.Logger
	reconnect      chan struct{}

	mu      sync.Mutex
	token   string
	botID   string
	client  *discordgo.Session
	running bool
	closed  bool
}

func (runtime *SessionRuntime) HasSession() bool {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.token != ""
}

func (runtime *SessionRuntime) DiscordBotID() string {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.botID
}

// Run links request.Token, or resumes the saved token, then stays connected
// until ctx ends. A token Discord rejects on resume ends the run as revoked.
func (runtime *SessionRuntime) Run(ctx context.Context, request control.SessionRunRequest, emit func(control.SessionRuntimeEvent)) error {
	if emit == nil {
		return agent.NewError(agent.ErrorInvalidArgument, "run Discord session", errors.New("event callback is required"))
	}
	runtime.mu.Lock()
	switch {
	case runtime.closed:
		runtime.mu.Unlock()
		return agent.NewError(agent.ErrorNotReady, "run Discord session", errors.New("session runtime is closed"))
	case runtime.running:
		runtime.mu.Unlock()
		return agent.NewError(agent.ErrorConflict, "run Discord session", errors.New("session runtime is already running"))
	}
	runtime.running = true
	saved := runtime.token
	runtime.mu.Unlock()
	defer func() {
		runtime.mu.Lock()
		runtime.running = false
		runtime.mu.Unlock()
		runtime.disconnect()
	}()

	token := saved
	switch request.Mode {
	case control.SessionRunLink:
		token = strings.TrimSpace(request.Token)
		if token == "" {
			return agent.NewError(agent.ErrorInvalidArgument, "link Discord bot", errors.New("a bot token is required"))
		}
		emit(control.SessionRuntimeEvent{State: control.RuntimeLinking})
	default:
		if saved == "" {
			return agent.NewError(agent.ErrorIntegrityFailure, "resume Discord session", errors.New("the saved bot token is missing"))
		}
		emit(control.SessionRuntimeEvent{State: control.RuntimeConnecting})
	}

	user, err := runtime.connect(ctx, token)
	if err != nil {
		if request.Mode != control.SessionRunLink && isRevokedToken(err) {
			emit(control.SessionRuntimeEvent{State: control.RuntimeRevoked})
			return nil
		}
		return err
	}
	if request.Mode == control.SessionRunLink {
		if err := WriteToken(runtime.tokenPath, token); err != nil {
			return agent.NewError(agent.ErrorStorageFailure, "save Discord bot token", err)
		}
		runtime.mu.Lock()
		runtime.token = token
		runtime.mu.Unlock()
	}
	runtime.mu.Lock()
	runtime.botID = user.ID
	runtime.mu.Unlock()
	emit(control.SessionRuntimeEvent{State: control.RuntimeConnected, DiscordBotID: user.ID, BotName: user.Username})

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-runtime.reconnect:
			emit(control.SessionRuntimeEvent{State: control.RuntimeReconnecting})
			runtime.disconnect()
			user, err = runtime.connect(ctx, token)
			if err != nil {
				if isRevokedToken(err) {
					emit(control.SessionRuntimeEvent{State: control.RuntimeRevoked})
					return nil
				}
				return err
			}
			emit(control.SessionRuntimeEvent{State: control.RuntimeConnected, DiscordBotID: user.ID, BotName: user.Username})
		}
	}
}

// connect verifies token and opens the gateway with it.
func (runtime *SessionRuntime) connect(ctx context.Context, token string) (*discordgo.User, error) {
	client, err := newClient(token, sessionIntents, runtime.logger)
	if err != nil {
		return nil, err
	}
	verifyCtx, cancel := context.WithTimeout(ctx, runtime.connectTimeout)
	defer cancel()
	user, err := verifyBot(verifyCtx, client)
	if err != nil {
		return nil, err
	}
	if err := openGateway(ctx, client, runtime.connectTimeout); err != nil {
		return nil, err
	}
	runtime.mu.Lock()
	runtime.client = client
	runtime.mu.Unlock()
	return user, nil
}

func (runtime *SessionRuntime) disconnect() {
	runtime.mu.Lock()
	client := runtime.client
	runtime.client = nil
	runtime.mu.Unlock()
	if client != nil {
		_ = client.Close()
	}
}

// Logout forgets the saved token. Discord has no API that revokes a bot
// token; resetting it in the Developer Portal does.
func (runtime *SessionRuntime) Logout(context.Context) error {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.token == "" {
		return agent.NewError(agent.ErrorNotReady, "unlink Discord bot", errors.New("no bot token is saved"))
	}
	if err := DeleteToken(runtime.tokenPath); err != nil {
		return agent.NewError(agent.ErrorStorageFailure, "unlink Discord bot", err)
	}
	runtime.token = ""
	return nil
}

func (runtime *SessionRuntime) Reconnect() error {
	runtime.mu.Lock()
	running := runtime.running && !runtime.closed
	runtime.mu.Unlock()
	if !running {
		return agent.NewError(agent.ErrorNotReady, "reconnect Discord session", errors.New("session runtime is not running"))
	}
	select {
	case runtime.reconnect <- struct{}{}:
	default:
	}
	return nil
}

func (runtime *SessionRuntime) Close(context.Context) error {
	runtime.mu.Lock()
	runtime.closed = true
	runtime.mu.Unlock()
	runtime.disconnect()
	return nil
}

var _ control.SessionRuntimeFactory = (*SessionFactory)(nil)
var _ control.ManagedSession = (*SessionRuntime)(nil)
