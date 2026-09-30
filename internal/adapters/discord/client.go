package discord

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gorilla/websocket"

	"github.com/Chomosuke9/DiscordAgent-Go/internal/agent"
)

// agentIntents are the gateway events the Agent needs. Message Content is a
// privileged intent: it must be switched on for the bot in the Developer
// Portal, or Discord refuses the connection.
const agentIntents = discordgo.IntentsGuilds | discordgo.IntentsGuildMessages | discordgo.IntentsDirectMessages |
	discordgo.IntentsMessageContent | discordgo.IntentsGuildEmojis

// sessionIntents are enough to show the bot online while no Agent runs.
const sessionIntents = discordgo.IntentsGuilds

// errTokenRejected is the cause of every error that means Discord no longer
// accepts the bot token.
var errTokenRejected = errors.New("Discord rejected the bot token; copy it again from the Developer Portal, or reset it there")

var loggerOnce sync.Once

// routeLibraryLogs sends discordgo's own log lines to slog. discordgo keeps
// its logger in a package variable, so this runs once per process.
func routeLibraryLogs(logger *slog.Logger) {
	loggerOnce.Do(func() {
		if logger == nil {
			logger = slog.Default()
		}
		discordgo.Logger = func(level, _ int, format string, arguments ...interface{}) {
			message := strings.TrimSpace(fmt.Sprintf(format, arguments...))
			switch level {
			case discordgo.LogError:
				logger.Warn("Discord library error", "detail", message)
			case discordgo.LogWarning:
				logger.Info("Discord library warning", "detail", message)
			default:
				logger.Debug("Discord library", "detail", message)
			}
		}
	})
}

// newClient builds a discordgo session for token. It does not connect.
func newClient(token string, intents discordgo.Intent, logger *slog.Logger) (*discordgo.Session, error) {
	routeLibraryLogs(logger)
	client, err := discordgo.New("Bot " + token)
	if err != nil {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "create Discord client", err)
	}
	client.Identify.Intents = intents
	client.StateEnabled = true
	client.State.MaxMessageCount = 0
	client.ShouldReconnectOnError = true
	client.ShouldRetryOnRateLimit = true
	client.LogLevel = discordgo.LogWarning
	client.Client = &http.Client{Timeout: 30 * time.Second}
	return client, nil
}

// verifyBot checks the token against the REST API before the gateway, so a
// wrong token fails with a clear reason. It returns the bot's user.
func verifyBot(ctx context.Context, client *discordgo.Session) (*discordgo.User, error) {
	user, err := client.User("@me", discordgo.WithContext(ctx))
	if err != nil {
		var restErr *discordgo.RESTError
		if errors.As(err, &restErr) && restErr.Response != nil && restErr.Response.StatusCode == http.StatusUnauthorized {
			return nil, agent.NewError(agent.ErrorPermissionDenied, "verify Discord bot token", errTokenRejected)
		}
		return nil, providerError(ctx, "verify Discord bot token", err)
	}
	if user == nil || user.ID == "" {
		return nil, agent.NewError(agent.ErrorProviderFailure, "verify Discord bot token", errors.New("Discord returned no bot user"))
	}
	if !user.Bot {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "verify Discord bot token", errors.New("the token belongs to a user account; use a bot token"))
	}
	return user, nil
}

// openGateway connects client, giving up after timeout or when ctx ends.
func openGateway(ctx context.Context, client *discordgo.Session, timeout time.Duration) error {
	result := make(chan error, 1)
	go func() { result <- client.Open() }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-result:
		if err != nil {
			return gatewayError(err)
		}
		return nil
	case <-timer.C:
		go func() {
			if <-result == nil {
				_ = client.Close()
			}
		}()
		return agent.NewError(agent.ErrorTimeout, "connect to Discord", context.DeadlineExceeded)
	case <-ctx.Done():
		go func() {
			if <-result == nil {
				_ = client.Close()
			}
		}()
		return agent.NewError(agent.ErrorCancelled, "connect to Discord", ctx.Err())
	}
}

// gatewayError explains the close codes a misconfigured bot gets.
func gatewayError(err error) error {
	var closeErr *websocket.CloseError
	if errors.As(err, &closeErr) {
		switch closeErr.Code {
		case 4004:
			return agent.NewError(agent.ErrorPermissionDenied, "connect to Discord", errTokenRejected)
		case 4013, 4014:
			return agent.NewError(agent.ErrorPermissionDenied, "connect to Discord", errors.New("turn on the Message Content intent for the bot in the Discord Developer Portal"))
		}
	}
	return agent.NewError(agent.ErrorUnavailable, "connect to Discord", err)
}

// isRevokedToken reports whether err means Discord no longer accepts the
// token.
func isRevokedToken(err error) bool {
	if errors.Is(err, errTokenRejected) {
		return true
	}
	var restErr *discordgo.RESTError
	return errors.As(err, &restErr) && restErr.Response != nil && restErr.Response.StatusCode == http.StatusUnauthorized
}

// providerError classifies a failed Discord call. A request that ran out of
// time may still have reached Discord, so it is a timeout, not a rejection.
func providerError(ctx context.Context, operation string, err error) error {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded):
		return agent.NewError(agent.ErrorTimeout, operation, err)
	case errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled):
		return agent.NewError(agent.ErrorCancelled, operation, err)
	}
	var restErr *discordgo.RESTError
	if errors.As(err, &restErr) && restErr.Response != nil {
		switch restErr.Response.StatusCode {
		case http.StatusForbidden:
			return agent.NewError(agent.ErrorPermissionDenied, operation, errors.New(restMessage(restErr, "the bot lacks the Discord permission for this")))
		case http.StatusNotFound:
			return agent.NewError(agent.ErrorNotFound, operation, errors.New(restMessage(restErr, "Discord could not find it")))
		case http.StatusBadRequest:
			return agent.NewError(agent.ErrorInvalidArgument, operation, errors.New(restMessage(restErr, "Discord rejected the request")))
		case http.StatusUnauthorized:
			return agent.NewError(agent.ErrorPermissionDenied, operation, errors.New("Discord no longer accepts the bot token"))
		}
	}
	return agent.NewError(agent.ErrorProviderFailure, operation, err)
}

func restMessage(err *discordgo.RESTError, fallback string) string {
	if err.Message != nil && strings.TrimSpace(err.Message.Message) != "" {
		return "Discord: " + strings.TrimSpace(err.Message.Message)
	}
	return fallback
}
