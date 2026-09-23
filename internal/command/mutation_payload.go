package command

import "github.com/Chomosuke9/WazzapAgent-Go/internal/agent"

// PromptMutation is the durable journal state shared with storage. Command
// syntax and behavior live in internal/inbound/commands/prompt.go.
type PromptMutation struct {
	ExpectedVersion agent.ConfigVersion
	AppliedVersion  agent.ConfigVersion
}

// PermissionCommand is a storage payload, not a command parser. The complete
// /permission handler lives in internal/inbound/commands/permission.go.
type PermissionCommandKind uint8

const (
	PermissionInvalid PermissionCommandKind = iota + 1
	PermissionView
	PermissionSet
)

type PermissionCommand struct {
	Kind  PermissionCommandKind
	Level agent.ModerationLevel
}

// TriggerCommand is a durable storage payload for /trigger. Command grammar
// and behavior live in internal/inbound/commands/trigger.go.
type TriggerCommandKind uint8

const (
	TriggerInvalid TriggerCommandKind = iota + 1
	TriggerView
	TriggerSetMention
	TriggerSetName
	TriggerSetReply
	TriggerSetRegex
	TriggerSetPattern
)

type TriggerCommand struct {
	Kind    TriggerCommandKind
	Enabled bool
	Pattern string
}

// PromptCommand is a storage payload, not a command parser. The complete
// /prompt handler lives in internal/inbound/commands/prompt.go.
type PromptCommandKind uint8

const (
	PromptInvalid PromptCommandKind = iota + 1
	PromptView
	PromptSet
	PromptClear
)

type PromptCommand struct {
	Kind PromptCommandKind
	Text string
}
