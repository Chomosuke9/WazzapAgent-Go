// Package command is the slash-command framework. Each command lives in one
// file under internal/inbound/commands and registers a Command; the Registry
// routes "/name args" to it, and Context is everything the command may use.
// Commands never import each other, and nothing outside their file has to
// change when one is added.
package command

import (
	"context"
	"time"

	"github.com/Chomosuke9/DiscordAgent-Go/internal/action"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/agent"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/conversation"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/identity"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/policy"
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
	// Run executes the command. Taps on buttons and menus this command sent
	// arrive here too, as "/<Name> <Button.Args>".
	Run func(ctx context.Context, c *Context) error
}

// Button is a quick-reply button, or a row in a Menu. A tap runs
// "/<Command> <Args>" through the router, so the tapper's own permission is
// checked as if they typed it.
type Button struct {
	Label string
	Args  string
	// Command is the command a tap runs; empty means the one that sent it.
	Command string
	// Description is a second line under the label, shown only in a Menu.
	Description string
}

// Menu is a list button: tapping Title opens Options, and picking one
// runs it like a Button tap.
type Menu struct {
	Title   string
	Options []Button
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
	Tasks   TaskScheduler
	// AssistantName is the bot's configured display name, used to write
	// its "@Name (Bot)" mention.
	AssistantName string
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

// TaskScheduler runs a prompt as an AI turn in a chat later, once or every
// day. Tasks are saved, so they still run after a restart. source is the
// command message that asked for one: scheduling twice for one source keeps
// one task, and the second call returns a zero Task.
type TaskScheduler interface {
	ScheduleTask(ctx context.Context, key agent.Key, source identity.MessageID, fireAt time.Time, prompt string) (Task, error)
	// ScheduleDailyTask runs prompt every day at minute (minutes after
	// midnight, in the bot's time zone).
	ScheduleDailyTask(ctx context.Context, key agent.Key, source identity.MessageID, minute int, prompt string) (Task, error)
	// Tasks lists the chat's tasks, soonest first.
	Tasks(ctx context.Context, key agent.Key) ([]Task, error)
	// CancelTask deletes the chat's one-off or daily task with this code. It
	// reports false when there is none.
	CancelTask(ctx context.Context, key agent.Key, code string, daily bool) (bool, error)
}

// Task is a saved task as commands show it.
type Task struct {
	// Code is the task's short ID, typed to delete it.
	Code   string
	Prompt string
	// FireAt is the next run, in the bot's time zone.
	FireAt time.Time
	Daily  bool
}

// Store is the durable inbox a command message came from. The registry uses
// it to mark the message handled. Commands never call it.
type Store interface {
	MarkCommandHandled(context.Context, conversation.IncomingMessage) error
	RawQuotedMessageReader
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
