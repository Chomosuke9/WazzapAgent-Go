package control

import (
	"context"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/config"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

// SettingsRepository is the only persistence dependency needed by the
// settings controller. Implementations own serialization and storage details;
// the controller only ever handles the typed settings value.
type SettingsRepository interface {
	Load(context.Context) (SettingsSnapshot, error)
	Save(context.Context, uint64, config.Settings) (SettingsSnapshot, error)
}

// SessionBindingRepository persists only the active WhatsApp scope and
// account identity. Pending scope writes are separate from settings revision.
type SessionBindingRepository interface {
	LoadSessionBinding(context.Context) (SessionBinding, error)
	BeginSessionPairing(context.Context, SessionScope) error
	MarkSessionPaired(context.Context, SessionScope, string) error
	AbortSessionPairing(context.Context, SessionScope) error
	MarkSessionRevoked(context.Context, SessionScope) error
}

// SessionRuntimeFactory creates a session-only client from a validated
// settings snapshot. Implementations must not bind an inbound handler or
// expose message sending.
type SessionRuntimeFactory interface {
	OpenSession(context.Context, config.Snapshot) (ManagedSession, error)
}

// ManagedSession is deliberately smaller than the bot runtime: it can only
// connect, observe status, request a link code, and unlink its own device.
type ManagedSession interface {
	HasSession() bool
	WhatsAppAccountID() string
	Run(context.Context, SessionRunRequest, func(SessionRuntimeEvent)) error
	Logout(context.Context) error
	Reconnect() error
	Close(context.Context) error
}

// SessionEventSink must enqueue without blocking a provider callback.
type SessionEventSink interface {
	TryPublish(SessionEvent) bool
}

// SessionScopeResolver resolves the durable IDs used for the WhatsApp device
// database. It runs only after the caller already owns the data-root lease.
type SessionScopeResolver interface {
	ResolveSessionSnapshot(context.Context, string, config.Settings, SessionScope) (config.Snapshot, error)
}

// AgentRuntimeFactory creates a bot runtime from the immutable configuration
// snapshot selected by AgentController. Unlike SessionRuntimeFactory, the
// returned runtime may receive and respond to WhatsApp messages.
type AgentRuntimeFactory interface {
	OpenAgentRuntime(context.Context, config.Snapshot) (ManagedAgentRuntime, error)
}

// ManagedAgentRuntime is the process-owned bot runtime used by the GUI.
// Snapshot contains only runtime readiness and connection state, never config
// values or provider credentials.
type ManagedAgentRuntime interface {
	Run(context.Context) error
	Close(context.Context) error
	Snapshot() AgentRuntimeSnapshot
}

// AgentSessionControl lets bot startup stop a session-only client before
// creating its own WhatsApp client. This preserves the one-client-per-root rule.
type AgentSessionControl interface {
	GetStatus(context.Context) (SessionStatus, error)
	Stop(context.Context) (SessionStatus, error)
}

type SessionScope struct {
	TenantID  identity.TenantID
	AccountID identity.AccountID
}
