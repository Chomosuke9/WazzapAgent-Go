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
	"github.com/Chomosuke9/WazzapAgent-Go/internal/sticker"
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
	Media   MediaDownloader
	// Stickers sends stickers; Catalog is each chat's named sticker store.
	Stickers StickerSender
	Catalog  sticker.Catalog
	Tasks    TaskScheduler
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

// MediaKind is what kind of media a command received.
type MediaKind uint8

const (
	MediaImage MediaKind = iota + 1
	MediaVideo
	MediaSticker
)

// Media is the image, video or sticker a command message carried or replied to.
type Media struct {
	Kind MediaKind
	// Data is the downloaded file. It is empty for a Lottie sticker.
	Data []byte
	// Animated is set for animated stickers and GIF-style videos.
	Animated bool
	// Lottie is set for WhatsApp's Lottie (premium) stickers: the provider
	// payload that resends the sticker unchanged. It is opaque to commands.
	Lottie []byte
}

// MediaDownloader fetches media from a payload the adapter captured with the
// command message.
type MediaDownloader interface {
	DownloadMedia(ctx context.Context, payload []byte) (Media, error)
}

// StickerSender sends a sticker to a chat, quoting quoted when it is set.
type StickerSender interface {
	SendSticker(ctx context.Context, key agent.Key, sticker sticker.Sticker, quoted identity.MessageID) error
}

// TaskScheduler runs a prompt as an AI turn in a chat at a later time. The
// task is saved, so it still runs after a restart. source is the command
// message that asked for it: scheduling twice for one source keeps one task.
type TaskScheduler interface {
	ScheduleTask(ctx context.Context, key agent.Key, source identity.MessageID, fireAt time.Time, prompt string) error
}

// Store is the durable inbox a command message came from. The registry uses
// it to mark the message handled. Commands never call it.
type Store interface {
	MarkCommandHandled(context.Context, conversation.IncomingMessage) error
	RawQuotedMessageReader
	// ReadCommandMedia returns the media payload captured with the command,
	// or an agent.ErrorNotFound error when it carried none.
	ReadCommandMedia(context.Context, conversation.IncomingMessage) ([]byte, error)
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
