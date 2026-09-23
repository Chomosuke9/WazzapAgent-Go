package control

import (
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
)

type SessionBindingState string

const (
	SessionUnpaired SessionBindingState = "unpaired"
	SessionPaired   SessionBindingState = "paired"
	SessionRevoked  SessionBindingState = "revoked"
)

type SessionRuntimeState string

const (
	RuntimeStopped      SessionRuntimeState = "stopped"
	RuntimeStarting     SessionRuntimeState = "starting"
	RuntimePairing      SessionRuntimeState = "pairing"
	RuntimeConnecting   SessionRuntimeState = "connecting"
	RuntimeConnected    SessionRuntimeState = "connected"
	RuntimeReconnecting SessionRuntimeState = "reconnecting"
	RuntimeStopping     SessionRuntimeState = "stopping"
	RuntimeLoggingOut   SessionRuntimeState = "logging_out"
	RuntimeRevoked      SessionRuntimeState = "revoked"
	RuntimeFailed       SessionRuntimeState = "failed"
)

type PairingMethod string

const (
	PairingQR        PairingMethod = "qr"
	PairingPhoneCode PairingMethod = "phone_code"
)

type SessionRunMode string

const (
	SessionRunResume  SessionRunMode = "resume"
	SessionRunPairing SessionRunMode = "pairing"
)

type SessionBinding struct {
	State             SessionBindingState
	ActiveScope       SessionScope
	HasActiveScope    bool
	WhatsAppAccountID string
	PendingScope      SessionScope
	HasPendingScope   bool
	UpdatedAt         time.Time
}

type SessionRunRequest struct {
	Mode   SessionRunMode
	Method PairingMethod
	Phone  string
}

// SessionPairing contains short-lived link material. It is never persisted or
// written to logs. QRCodeDataURL is generated locally from the provider code.
type SessionPairing struct {
	Method        PairingMethod
	Code          string
	QRCodeDataURL string
	Generation    uint64
	ExpiresAt     time.Time
}

type SessionRuntimeEvent struct {
	State             SessionRuntimeState
	Pairing           *SessionPairing
	WhatsAppAccountID string
	ErrorCode         agent.ErrorCode
}

type SessionStatus struct {
	BindingState      SessionBindingState
	RuntimeState      SessionRuntimeState
	SessionPresent    bool
	WhatsAppAccountID string
	OperationID       string
	Pairing           *SessionPairing
	ErrorCode         agent.ErrorCode
}

type SessionEvent struct {
	OperationID string
	Status      SessionStatus
}

type SessionOperation struct {
	OperationID string
	Status      SessionStatus
}

type BeginPairingRequest struct {
	Method PairingMethod
	Phone  string
}
