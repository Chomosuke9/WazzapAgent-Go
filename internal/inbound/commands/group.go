package commands

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

func init() {
	register(command.Command{
		Name:        "group",
		Permission:  "admin and group and !fromMe",
		Description: "Manages group status, description, messages, mutes, and members.",
		DeniedReply: "The /group command can only be used by a group admin.",
		Run:         runGroup,
	})
}

const groupUsage = "Usage: /group close, /group open, /group description <text>, /group delete as a reply to a message, /group mute @Name (senderRef) <minutes>, or /group kick @Name (senderRef)."

type groupAction struct {
	kind        string // close, open, description, delete, mute, kick
	description string
	member      identity.SenderRef
	minutes     uint32
}

func runGroup(ctx context.Context, c *command.Context) error {
	moderator, err := c.Group()
	if err != nil {
		return err
	}
	parsed, err := parseGroupArgs(c.Args)
	if err != nil {
		return c.Reply(ctx, groupUsage)
	}
	var target identity.MessageID
	if parsed.kind == "delete" {
		if c.Message.Quote == nil || c.Message.Quote.ID.IsZero() {
			return c.Reply(ctx, groupUsage)
		}
		target = c.Message.Quote.ID
	}
	key := c.Key()
	switch parsed.kind {
	case "close", "open":
		err = moderator.SetGroupAnnounce(ctx, key, parsed.kind == "close")
	case "description":
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
		// The result is visible in the group; a confirmation would be noise.
		return nil
	}
	return c.Reply(ctx, fmt.Sprintf("The /group %s command completed successfully.", parsed.kind))
}

func parseGroupArgs(args string) (groupAction, error) {
	if len(args) > 1024 {
		return groupAction{}, errors.New("command exceeds maximum length")
	}
	fields := strings.Fields(args)
	if len(fields) == 0 {
		return groupAction{}, errors.New("subcommand is required")
	}
	switch kind := fields[0]; kind {
	case "close", "open", "delete":
		if len(fields) != 1 {
			return groupAction{}, errors.New("subcommand does not accept arguments")
		}
		return groupAction{kind: kind}, nil
	case "description":
		value := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(args), "description"))
		if value == "" {
			return groupAction{}, errors.New("description is required")
		}
		return groupAction{kind: kind, description: value}, nil
	case "mute":
		if len(fields) < 3 {
			return groupAction{}, errors.New("member and duration are required")
		}
		member, err := parseMemberRef(fields[len(fields)-2])
		if err != nil {
			return groupAction{}, err
		}
		minutes, err := strconv.ParseUint(fields[len(fields)-1], 10, 32)
		if err != nil || minutes > 43200 {
			return groupAction{}, errors.New("duration must be 0-43200 minutes")
		}
		return groupAction{kind: kind, member: member, minutes: uint32(minutes)}, nil
	case "kick":
		if len(fields) < 2 {
			return groupAction{}, errors.New("member is required")
		}
		member, err := parseMemberRef(fields[len(fields)-1])
		if err != nil {
			return groupAction{}, err
		}
		return groupAction{kind: kind, member: member}, nil
	default:
		return groupAction{}, errors.New("subcommand is unsupported")
	}
}

// parseMemberRef reads the "(senderRef)" that follows a mention.
func parseMemberRef(value string) (identity.SenderRef, error) {
	return identity.ParseSenderRef(strings.ToLower(strings.Trim(value, "()")))
}
