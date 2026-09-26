// Package command is the slash-command framework. Each command lives in one
// file under internal/inbound/commands and registers a Command; the Registry
// routes "/name args" to it, and Context is everything the command may use.
// Commands never import each other, and nothing outside their file has to
// change when one is added.
package command

import (
	"context"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/action"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/policy"
)

// Command is one slash command.
type Command struct {
	// Name is the canonical token: lowercase, without the slash.
	Name    string
	Aliases []string
	// Permission is a boolean expression over public, owner, admin, group,
	// private and fromMe, evaluated for every invocation and button tap.
	Permission  string
	Description string
	// DeniedReply is sent when Permission denies the sender. Optional.
	DeniedReply string
	// Run executes the command. Taps on buttons this command sent with
	// Context.ReplyButtons arrive here too, as "/<Name> <Button.Args>".
	Run func(ctx context.Context, c *Context) error
}

// Button is a quick-reply button owned by the command that sends it. A tap
// runs that command again with Args, under the same Permission.
type Button struct {
	Label string
	Args  string
}

// PermissionFacts are the trusted facts a Permission expression is evaluated
// against. They come from the policy boundary, never from message text.
type PermissionFacts = policy.PermissionFacts

// Platform holds the provider ports the process lends to commands. A nil
// port means the capability is unavailable in this host.
type Platform struct {
	Text    TextSender
	Buttons ButtonSender
	Group   GroupModerator
}

type TextSender interface {
	SendText(context.Context, action.SendTextRequest) (action.SendTextResult, error)
}

type ButtonSender interface {
	SendButtons(context.Context, action.SendButtonsRequest) (action.SendTextResult, error)
}

// GroupModerator is the provider-neutral port for group administration.
// Implementations own resolving agent identities to provider addresses.
type GroupModerator interface {
	// SetGroupAnnounce restricts sending to admins when announce is true.
	SetGroupAnnounce(ctx context.Context, key agent.Key, announce bool) error
	SetGroupDescription(ctx context.Context, key agent.Key, description string) error
	// RevokeGroupMessage deletes a stored message for every group member.
	RevokeGroupMessage(ctx context.Context, key agent.Key, target identity.MessageID) error
	// RemoveGroupMember kicks the member addressed by ref from the group.
	RemoveGroupMember(ctx context.Context, key agent.Key, ref identity.SenderRef) error
	// MuteGroupMember silences ref for minutes starting at now; zero unmutes.
	MuteGroupMember(ctx context.Context, key agent.Key, ref identity.SenderRef, minutes uint32, now time.Time) error
}

// Store is the durable inbox a command message came from. The registry uses
// it to mark the message handled and to journal config writes so a replay
// after a crash never applies a change twice. Commands never call it.
type Store interface {
	MarkCommandHandled(context.Context, conversation.IncomingMessage) error
	BeginConfigMutation(context.Context, conversation.IncomingMessage, agent.ConfigVersion) (ConfigMutation, error)
	MarkConfigMutationApplied(context.Context, conversation.IncomingMessage, agent.ConfigVersion, agent.ConfigVersion) error
	RawQuotedMessageReader
}

// ConfigMutation is the journal state of one command's config write.
type ConfigMutation struct {
	ExpectedVersion agent.ConfigVersion
	AppliedVersion  agent.ConfigVersion
}

type Observer interface {
	ObserveHistoryReset()
}

// Invocation is what the host knows about one command message.
type Invocation struct {
	Agent    *agent.Agent
	Config   agent.ConfigSnapshot
	Message  conversation.IncomingMessage
	Facts    PermissionFacts
	Platform Platform
	// Store is nil for commands that did not come from the durable inbox,
	// such as commands issued by the model inside a reply.
	Store    Store
	Observer Observer
}
