// Package groupcmd owns the closed grammar for model-carried group commands.
package groupcmd

import (
	"errors"
	"strconv"
	"strings"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

type Kind string

const (
	Close       Kind = "close"
	Open        Kind = "open"
	Description Kind = "description"
	Delete      Kind = "delete"
	Mute        Kind = "mute"
	Kick        Kind = "kick"
)

type Command struct {
	Kind        Kind
	Description string
	SenderRef   identity.SenderRef
	Duration    uint32
}

func Parse(raw string) (Command, error) {
	if len(raw) > 1024 {
		return Command{}, errors.New("command exceeds maximum length")
	}
	normalized := strings.TrimSpace(raw)
	if normalized != "" && !strings.HasPrefix(normalized, "/") {
		normalized = "/" + normalized
	}
	fields := strings.Fields(normalized)
	if len(fields) < 2 || fields[0] != "/group" {
		return Command{}, errors.New("command is malformed")
	}
	switch Kind(fields[1]) {
	case Close, Open, Delete:
		if len(fields) != 2 {
			return Command{}, errors.New("command does not accept arguments")
		}
		return Command{Kind: Kind(fields[1])}, nil
	case Description:
		value := strings.TrimSpace(strings.TrimPrefix(normalized, "/group description"))
		if value == "" {
			return Command{}, errors.New("description is required")
		}
		return Command{Kind: Description, Description: value}, nil
	case Mute:
		if len(fields) < 4 {
			return Command{}, errors.New("member and duration are required")
		}
		ref, err := parseSenderRef(fields[len(fields)-2])
		if err != nil {
			return Command{}, err
		}
		duration, err := strconv.ParseUint(fields[len(fields)-1], 10, 32)
		if err != nil || duration > 43200 {
			return Command{}, errors.New("duration must be 0-43200 minutes")
		}
		return Command{Kind: Mute, SenderRef: ref, Duration: uint32(duration)}, nil
	case Kick:
		if len(fields) < 3 {
			return Command{}, errors.New("member is required")
		}
		ref, err := parseSenderRef(fields[len(fields)-1])
		if err != nil {
			return Command{}, err
		}
		return Command{Kind: Kick, SenderRef: ref}, nil
	default:
		return Command{}, errors.New("subcommand is unsupported")
	}
}

func (command Command) Capability() string { return "group." + string(command.Kind) }

func (command Command) ValidateTarget(target identity.MessageID) error {
	if command.Kind == Delete && target.IsZero() {
		return errors.New("delete requires a target message")
	}
	if command.Kind != Delete && !target.IsZero() {
		return errors.New("only delete accepts a target message")
	}
	return nil
}

func parseSenderRef(value string) (identity.SenderRef, error) {
	return identity.ParseSenderRef(strings.ToLower(strings.Trim(value, "()")))
}
