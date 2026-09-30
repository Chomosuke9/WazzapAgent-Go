package commands

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Chomosuke9/DiscordAgent-Go/internal/agent"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/command"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/identity"
)

func init() {
	register(command.Command{
		Name: "mod",
		// For the bot, admin means it may manage messages in the channel;
		// runMod also holds it to the chat's moderation level.
		Permission:  "admin and group",
		Description: "Locks or unlocks the channel, sets its topic, and deletes messages, mutes, or kicks members.",
		DeniedReply: "The /mod command can only be used by a server moderator.",
		Run:         runMod,
	})
}

const modUsage = "Usage: /mod lock, /mod unlock, /mod topic <text>, /mod delete as a reply to a message, /mod mute @Name (senderRef) <minutes>, or /mod kick @Name (senderRef)."

type modAction struct {
	kind        string // lock, unlock, topic, delete, mute, kick
	description string
	member      identity.SenderRef
	minutes     uint32
}

func runMod(ctx context.Context, c *command.Context) error {
	moderator, err := c.Group()
	if err != nil {
		return err
	}
	parsed, err := parseModArgs(c.Args)
	if err != nil {
		return c.Reply(ctx, modUsage)
	}
	if level := c.Config.Permission.ModerationLevel; c.Facts.FromMe && level < parsed.botLevel() {
		return c.Reply(ctx, fmt.Sprintf("My moderation level here is %d, so I can't %s. A server moderator can raise it with /permission.", level, parsed.verb()))
	}
	var target identity.MessageID
	if parsed.kind == "delete" {
		if c.Message.Quote == nil || c.Message.Quote.ID.IsZero() {
			return c.Reply(ctx, modUsage)
		}
		target = c.Message.Quote.ID
	}
	key := c.Key()
	switch parsed.kind {
	case "lock", "unlock":
		err = moderator.SetGroupAnnounce(ctx, key, parsed.kind == "lock")
	case "topic":
		err = moderator.SetGroupDescription(ctx, key, parsed.description)
	case "delete":
		err = moderator.RevokeGroupMessage(ctx, key, target)
	case "mute":
		err = moderator.MuteGroupMember(ctx, key, parsed.member, parsed.minutes, time.Now().UTC())
	case "kick":
		err = moderator.RemoveGroupMember(ctx, key, parsed.member)
	}
	if err != nil {
		return err
	}
	if parsed.kind == "delete" || parsed.kind == "kick" {
		// The result is visible in the channel; a confirmation would be noise.
		return nil
	}
	return c.Reply(ctx, fmt.Sprintf("The /mod %s command completed successfully.", parsed.kind))
}

// botLevel is the moderation level the bot needs for the action. People are
// held only to the command's permission.
func (parsed modAction) botLevel() agent.ModerationLevel {
	switch parsed.kind {
	case "delete":
		return agent.ModerationDelete
	case "mute":
		return agent.ModerationDeleteMute
	case "kick":
		return agent.ModerationDeleteMuteKick
	}
	return agent.ModerationNone
}

func (parsed modAction) verb() string {
	switch parsed.kind {
	case "delete":
		return "delete messages"
	case "mute":
		return "mute members"
	}
	return "remove members"
}

func parseModArgs(args string) (modAction, error) {
	if len(args) > 1024 {
		return modAction{}, errors.New("command exceeds maximum length")
	}
	fields := strings.Fields(args)
	if len(fields) == 0 {
		return modAction{}, errors.New("subcommand is required")
	}
	switch kind := fields[0]; kind {
	case "lock", "unlock", "delete":
		if len(fields) != 1 {
			return modAction{}, errors.New("subcommand does not accept arguments")
		}
		return modAction{kind: kind}, nil
	case "topic":
		value := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(args), "topic"))
		if value == "" {
			return modAction{}, errors.New("topic is required")
		}
		return modAction{kind: kind, description: value}, nil
	case "mute":
		if len(fields) < 3 {
			return modAction{}, errors.New("member and duration are required")
		}
		member, err := parseMemberRef(fields[len(fields)-2])
		if err != nil {
			return modAction{}, err
		}
		minutes, err := strconv.ParseUint(fields[len(fields)-1], 10, 32)
		if err != nil || minutes > 43200 {
			return modAction{}, errors.New("duration must be 0-43200 minutes")
		}
		return modAction{kind: kind, member: member, minutes: uint32(minutes)}, nil
	case "kick":
		if len(fields) < 2 {
			return modAction{}, errors.New("member is required")
		}
		member, err := parseMemberRef(fields[len(fields)-1])
		if err != nil {
			return modAction{}, err
		}
		return modAction{kind: kind, member: member}, nil
	default:
		return modAction{}, errors.New("subcommand is unsupported")
	}
}

// parseMemberRef reads the "(senderRef)" that follows a mention.
func parseMemberRef(value string) (identity.SenderRef, error) {
	return identity.ParseSenderRef(strings.ToLower(strings.Trim(value, "()")))
}
