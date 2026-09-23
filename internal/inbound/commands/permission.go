package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/action"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/policy"
)

var PermissionCommand = command.Descriptor{
	Name:        "permission",
	Aliases:     []string{"permissions"},
	Capability:  policy.CapabilityPermissionWrite,
	Permission:  "owner and !fromMe",
	Description: "Sets the moderation permission level from 0 to 3.",
	DeniedReply: "The /permission command can only be used by the configured owner.",
	Handler:     handlePermission,
}

func handlePermission(ctx context.Context, input command.Context, adapter command.Adapter) error {
	send := func(text string) error {
		actionID, err := identity.NewActionID()
		if err != nil {
			return agent.NewError(agent.ErrorInternal, "create permission response ID", err)
		}
		key := agent.Key{TenantID: input.Message.TenantID, AccountID: input.Message.AccountID, ChatID: input.Message.ChatID}
		if _, err := adapter.SendText(ctx, action.SendTextRequest{Key: key, ActionID: actionID, Text: text}); err != nil {
			return err
		}
		return input.Store.MarkCommandHandled(ctx, input.Message)
	}
	parsed := parsePermissionCommand(input.Message.Text)
	switch parsed.Kind {
	case command.PermissionView:
		return send(formatModerationLevel(input.Snapshot.Permission.ModerationLevel))
	case command.PermissionSet:
		_, err := applyPermissionMutation(ctx, input, parsed)
		if err != nil {
			return err
		}
		return send("Permission updated. " + formatModerationLevel(parsed.Level))
	case command.PermissionInvalid:
		return send("Usage: /permission 0, 1, 2, or 3. Level 0: no moderation; 1: delete; 2: delete+mute; 3: delete+mute+kick.")
	default:
		return agent.NewError(agent.ErrorIntegrityFailure, "handle permission command", fmt.Errorf("unknown command kind"))
	}
}

func parsePermissionCommand(raw string) command.PermissionCommand {
	_, rawArgument, argumentsPresent := strings.Cut(strings.TrimPrefix(raw, "/"), " ")
	argument := strings.TrimSpace(rawArgument)
	if !argumentsPresent || rawArgument == "view" {
		return command.PermissionCommand{Kind: command.PermissionView}
	}
	if len(argument) == 1 && argument[0] >= '0' && argument[0] <= '3' {
		return command.PermissionCommand{Kind: command.PermissionSet, Level: agent.ModerationLevel(argument[0] - '0')}
	}
	return command.PermissionCommand{Kind: command.PermissionInvalid}
}

func formatModerationLevel(level agent.ModerationLevel) string {
	labels := [...]string{
		"Level 0: moderation disabled.",
		"Level 1: delete.",
		"Level 2: delete dan mute.",
		"Level 3: delete, mute, dan kick.",
	}
	if !level.Valid() {
		return "Invalid permission level."
	}
	return labels[level]
}

func applyPermissionMutation(ctx context.Context, input command.Context, parsed command.PermissionCommand) (agent.ConfigVersion, error) {
	journal, err := input.Store.BeginPermissionMutation(ctx, input.Message, parsed, input.Snapshot.Version)
	if err != nil {
		return 0, err
	}
	if journal.AppliedVersion != 0 {
		return journal.AppliedVersion, nil
	}
	if input.Snapshot.Version == journal.ExpectedVersion+1 && permissionMutationMatches(input.Snapshot, parsed) {
		if err := input.Store.MarkPermissionMutationApplied(ctx, input.Message, journal.ExpectedVersion, input.Snapshot.Version); err != nil {
			return 0, err
		}
		return input.Snapshot.Version, nil
	}
	if input.Snapshot.Version != journal.ExpectedVersion {
		return 0, agent.NewError(agent.ErrorConflict, "apply permission mutation", fmt.Errorf("config changed after command authorization; resend the command"))
	}
	permission := input.Snapshot.Permission
	permission.ModerationLevel = parsed.Level
	updated, err := input.Agent.Config().SetPermission(ctx, input.Snapshot.Version, permission)
	if err != nil {
		return 0, err
	}
	if err := input.Store.MarkPermissionMutationApplied(ctx, input.Message, journal.ExpectedVersion, updated.Version); err != nil {
		return 0, err
	}
	return updated.Version, nil
}

func permissionMutationMatches(snapshot agent.ConfigSnapshot, parsed command.PermissionCommand) bool {
	return parsed.Kind == command.PermissionSet && snapshot.Permission.ModerationLevel == parsed.Level
}
