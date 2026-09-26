package commands

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/action"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/inbound/commands/groupcmd"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/policy"
)

var GroupCommand = command.Descriptor{
	Name:        "group",
	Capability:  policy.CapabilityCommandGroup,
	Permission:  "admin and group and !fromMe",
	Description: "Manages group status, description, messages, mutes, and members.",
	DeniedReply: "The /group command can only be used by a group admin.",
	Handler:     handleGroup,
}

func handleGroup(ctx context.Context, input command.Context, rawAdapter command.Adapter) error {
	moderator, ok := rawAdapter.(GroupModerator)
	if !ok || moderator == nil {
		return agent.NewError(agent.ErrorIntegrityFailure, "handle /group command", errors.New("group moderation is unavailable"))
	}
	markHandled := func() error {
		return input.Store.MarkCommandHandled(ctx, input.Message)
	}
	send := func(text string) error {
		actionID, err := identity.NewActionID()
		if err != nil {
			return agent.NewError(agent.ErrorInternal, "create group response ID", err)
		}
		key := agent.Key{TenantID: input.Message.TenantID, AccountID: input.Message.AccountID, ChatID: input.Message.ChatID}
		if _, err := rawAdapter.SendText(ctx, action.SendTextRequest{Key: key, ActionID: actionID, Text: text}); err != nil {
			return err
		}
		return markHandled()
	}
	raw := input.Message.Text
	parsed, err := groupcmd.Parse(raw)
	if err != nil {
		return send(groupCommandUsage())
	}
	var target identity.MessageID
	if parsed.Kind == groupcmd.Delete && input.Message.Quote != nil {
		target = input.Message.Quote.ID
	}
	key := agent.Key{TenantID: input.Message.TenantID, AccountID: input.Message.AccountID, ChatID: input.Message.ChatID}
	if err := HandleGroup(ctx, moderator, key, raw, target, time.Now().UTC()); err != nil {
		if agent.IsCode(err, agent.ErrorInvalidArgument) {
			return send(groupCommandUsage())
		}
		return err
	}
	if parsed.Kind == groupcmd.Delete || parsed.Kind == groupcmd.Kick {
		return markHandled()
	}
	return send(fmt.Sprintf("The /group %s command completed successfully.", parsed.Kind))
}

func groupCommandUsage() string {
	return "Usage: /group close, /group open, /group description <text>, /group delete as a reply to a message, /group mute @Name (senderRef) <minutes>, or /group kick @Name (senderRef)."
}

// GroupModerator is the provider-neutral port the /group family executes
// against. Implementations own resolving agent identities to provider
// addresses and translating provider failures into agent errors.
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

// HandleGroup owns parsing and dispatch for the complete /group family.
// Successful commands have no result value.
func HandleGroup(ctx context.Context, moderator GroupModerator, key agent.Key, raw string, targetMessageID identity.MessageID, now time.Time) error {
	if moderator == nil || now.IsZero() {
		return agent.NewError(agent.ErrorInvalidArgument, "handle /group command", errors.New("moderator and time are required"))
	}
	if err := key.Validate(); err != nil {
		return agent.NewError(agent.ErrorInvalidArgument, "handle /group command", err)
	}
	parsed, err := groupcmd.Parse(raw)
	if err != nil {
		return agent.NewError(agent.ErrorInvalidArgument, "handle /group command", err)
	}
	if err := parsed.ValidateTarget(targetMessageID); err != nil {
		return agent.NewError(agent.ErrorInvalidArgument, "handle /group command", err)
	}

	switch parsed.Kind {
	case groupcmd.Close, groupcmd.Open:
		return moderator.SetGroupAnnounce(ctx, key, parsed.Kind == groupcmd.Close)
	case groupcmd.Description:
		return moderator.SetGroupDescription(ctx, key, parsed.Description)
	case groupcmd.Delete:
		return moderator.RevokeGroupMessage(ctx, key, targetMessageID)
	case groupcmd.Mute:
		return moderator.MuteGroupMember(ctx, key, parsed.SenderRef, parsed.Duration, now)
	case groupcmd.Kick:
		return moderator.RemoveGroupMember(ctx, key, parsed.SenderRef)
	default:
		return agent.NewError(agent.ErrorIntegrityFailure, "handle /group command", errors.New("parsed command kind is invalid"))
	}
}
