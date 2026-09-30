package control

import (
	"time"

	"github.com/Chomosuke9/DiscordAgent-Go/internal/agent"
)

type SessionBindingState string

const (
	SessionUnlinked SessionBindingState = "unlinked"
	SessionLinked   SessionBindingState = "linked"
	SessionRevoked  SessionBindingState = "revoked"
)

type SessionRuntimeState string

const (
	RuntimeStopped      SessionRuntimeState = "stopped"
	RuntimeStarting     SessionRuntimeState = "starting"
	RuntimeLinking      SessionRuntimeState = "linking"
	RuntimeConnecting   SessionRuntimeState = "connecting"
	RuntimeConnected    SessionRuntimeState = "connected"
	RuntimeReconnecting SessionRuntimeState = "reconnecting"
	RuntimeStopping     SessionRuntimeState = "stopping"
	RuntimeLoggingOut   SessionRuntimeState = "logging_out"
	RuntimeRevoked      SessionRuntimeState = "revoked"
	RuntimeFailed       SessionRuntimeState = "failed"
)

type SessionRunMode string

const (
	SessionRunResume SessionRunMode = "resume"
	SessionRunLink   SessionRunMode = "link"
)

type SessionBinding struct {
	State           SessionBindingState
	ActiveScope     SessionScope
	HasActiveScope  bool
	DiscordBotID    string
	PendingScope    SessionScope
	HasPendingScope bool
	UpdatedAt       time.Time
}

// SessionRunRequest starts a session. Token is set only when linking: it is
// the bot token to verify and save. It is never persisted outside the
// account scope's token file or written to logs.
type SessionRunRequest struct {
	Mode  SessionRunMode
	Token string
}

type SessionRuntimeEvent struct {
	State        SessionRuntimeState
	DiscordBotID string
	BotName      string
	ErrorCode    agent.ErrorCode
}

type SessionStatus struct {
	BindingState   SessionBindingState
	RuntimeState   SessionRuntimeState
	SessionPresent bool
	DiscordBotID   string
	// BotName is the bot's Discord username, known once it has connected in
	// this process.
	BotName     string
	OperationID string
	ErrorCode   agent.ErrorCode
}

type SessionEvent struct {
	OperationID string
	Status      SessionStatus
}

type SessionOperation struct {
	OperationID string
	Status      SessionStatus
}

// BeginLinkRequest links a Discord bot by its token.
type BeginLinkRequest struct {
	Token string
}
