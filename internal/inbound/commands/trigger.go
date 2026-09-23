package commands

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/action"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/policy"
)

var TriggerCommand = command.Descriptor{
	Name:        "trigger",
	Capability:  policy.CapabilityTriggerWrite,
	Permission:  "(owner or isAdmin) and !fromMe and isGroup",
	Description: "Configures Agent triggers per chat. Only the owner or a group admin can use it in a group.",
	DeniedReply: "The /trigger command can only be used by the owner or an admin in a group.",
	Handler:     handleTrigger,
}

func handleTrigger(ctx context.Context, input command.Context, adapter command.Adapter) error {
	send := func(text string) error {
		actionID, err := identity.NewActionID()
		if err != nil {
			return agent.NewError(agent.ErrorInternal, "create trigger response ID", err)
		}
		key := agent.Key{TenantID: input.Message.TenantID, AccountID: input.Message.AccountID, ChatID: input.Message.ChatID}
		if _, err := adapter.SendText(ctx, action.SendTextRequest{Key: key, ActionID: actionID, Text: text}); err != nil {
			return err
		}
		return input.Store.MarkCommandHandled(ctx, input.Message)
	}

	parsed := parseTriggerCommand(input.Message.Text)
	switch parsed.Kind {
	case command.TriggerView:
		return send(formatTriggers(input.Snapshot.Triggers))
	case command.TriggerSetMention, command.TriggerSetName, command.TriggerSetReply, command.TriggerSetRegex, command.TriggerSetPattern:
		updated, err := applyTriggerMutation(ctx, input, parsed)
		if err != nil {
			if agent.IsCode(err, agent.ErrorInvalidArgument) {
				return send(triggerUsage())
			}
			return err
		}
		return send("Chat triggers updated.\n" + formatTriggers(updated.Triggers))
	case command.TriggerInvalid:
		return send(triggerUsage())
	default:
		return agent.NewError(agent.ErrorIntegrityFailure, "handle trigger command", fmt.Errorf("unknown command kind"))
	}
}

func parseTriggerCommand(raw string) command.TriggerCommand {
	_, rawArgument, present := strings.Cut(strings.TrimPrefix(raw, "/"), " ")
	if !present || rawArgument == "view" {
		return command.TriggerCommand{Kind: command.TriggerView}
	}
	argument := strings.TrimSpace(rawArgument)
	field, value, hasValue := strings.Cut(argument, " ")
	value = strings.TrimSpace(value)
	if !hasValue {
		return command.TriggerCommand{Kind: command.TriggerInvalid}
	}
	kind := command.TriggerInvalid
	switch field {
	case "mention":
		kind = command.TriggerSetMention
	case "name":
		kind = command.TriggerSetName
	case "reply":
		kind = command.TriggerSetReply
	case "regex":
		kind = command.TriggerSetRegex
	case "pattern":
		if value != "" && utf8.ValidString(value) && len(value) <= agent.MaxTriggerPatternBytes {
			return command.TriggerCommand{Kind: command.TriggerSetPattern, Pattern: value}
		}
		return command.TriggerCommand{Kind: command.TriggerInvalid}
	default:
		return command.TriggerCommand{Kind: command.TriggerInvalid}
	}
	if value == "on" {
		return command.TriggerCommand{Kind: kind, Enabled: true}
	}
	if value == "off" {
		return command.TriggerCommand{Kind: kind, Enabled: false}
	}
	return command.TriggerCommand{Kind: command.TriggerInvalid}
}

func applyTriggerMutation(ctx context.Context, input command.Context, parsed command.TriggerCommand) (agent.ConfigSnapshot, error) {
	triggers := input.Snapshot.Triggers
	switch parsed.Kind {
	case command.TriggerSetMention:
		triggers.Mention = parsed.Enabled
	case command.TriggerSetName:
		triggers.Name = parsed.Enabled
	case command.TriggerSetReply:
		triggers.Reply = parsed.Enabled
	case command.TriggerSetRegex:
		triggers.NameRegex = parsed.Enabled
		if parsed.Enabled {
			triggers.Name = true
		}
	case command.TriggerSetPattern:
		triggers.Name = true
		triggers.NameRegex = true
		triggers.NamePattern = parsed.Pattern
	default:
		return agent.ConfigSnapshot{}, agent.NewError(agent.ErrorInvalidArgument, "apply trigger mutation", fmt.Errorf("command is not a mutation"))
	}
	values := input.Snapshot.Values()
	values.Triggers = triggers
	if err := agent.ValidateConfigValues(values); err != nil {
		return agent.ConfigSnapshot{}, err
	}
	journal, err := input.Store.BeginTriggerMutation(ctx, input.Message, parsed, input.Snapshot.Version)
	if err != nil {
		return agent.ConfigSnapshot{}, err
	}
	if journal.AppliedVersion != 0 {
		return input.Snapshot, nil
	}
	if input.Snapshot.Version == journal.ExpectedVersion+1 && input.Snapshot.Triggers == triggers {
		if err := input.Store.MarkTriggerMutationApplied(ctx, input.Message, journal.ExpectedVersion, input.Snapshot.Version); err != nil {
			return agent.ConfigSnapshot{}, err
		}
		return input.Snapshot, nil
	}
	if input.Snapshot.Version != journal.ExpectedVersion {
		return agent.ConfigSnapshot{}, agent.NewError(agent.ErrorConflict, "apply trigger mutation", fmt.Errorf("config changed after command authorization; resend the command"))
	}
	updated, err := input.Agent.Config().SetTriggers(ctx, input.Snapshot.Version, triggers)
	if err != nil {
		return agent.ConfigSnapshot{}, err
	}
	if err := input.Store.MarkTriggerMutationApplied(ctx, input.Message, journal.ExpectedVersion, updated.Version); err != nil {
		return agent.ConfigSnapshot{}, err
	}
	return updated, nil
}

func formatTriggers(triggers agent.TriggerConfig) string {
	name := "off"
	if triggers.Name {
		name = "on"
	}
	nameDetail := "Agent name"
	if triggers.NameRegex {
		nameDetail = "regex: " + strconv.Quote(triggers.NamePattern)
	}
	return fmt.Sprintf("Group triggers:\n• mention: %s\n• name: %s (%s)\n• reply to bot: %s",
		triggerState(triggers.Mention), name, nameDetail, triggerState(triggers.Reply))
}

func triggerState(enabled bool) string {
	if enabled {
		return "on"
	}
	return "off"
}

func triggerUsage() string {
	return fmt.Sprintf("Usage: /trigger view, /trigger mention on|off, /trigger name on|off, /trigger reply on|off, /trigger regex on|off, or /trigger pattern <regex>. The pattern uses Go regex syntax and automatically enables the name trigger and regex mode; maximum length is %d bytes.", agent.MaxTriggerPatternBytes)
}
