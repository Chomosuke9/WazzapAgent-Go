package commands

import (
	"context"
	"fmt"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/policy"
)

var PermissionCommand = command.Descriptor{
	Name:        "permission",
	Aliases:     []string{"permissions"},
	Capability:  policy.CapabilityPermissionWrite,
	Permission:  "owner and !fromMe",
	Description: "Mengatur permission moderasi level 0 sampai 3.",
	DeniedReply: "Perintah /permission hanya dapat digunakan oleh owner yang dikonfigurasi.",
	Handler:     handlePermission,
}

func handlePermission(ctx context.Context, request command.Request, input command.Context, adapter any) error {
	parsed, recognized := command.ParsePermissionCommand(command.CanonicalText(request))
	if !recognized {
		return agent.NewError(agent.ErrorIntegrityFailure, "handle permission command", fmt.Errorf("permission command was not parsed"))
	}
	switch parsed.Kind {
	case command.PermissionView:
		return input.Responses.Reply(ctx, input.Message, input.Snapshot.Version, command.FormatModerationLevel(input.Snapshot.Permission.ModerationLevel))
	case command.PermissionSet:
		updated, err := applyPermissionMutation(ctx, input, parsed)
		if err != nil {
			return err
		}
		return input.Responses.Reply(ctx, input.Message, updated, "Permission diperbarui. "+command.FormatModerationLevel(parsed.Level))
	case command.PermissionInvalid:
		return input.Responses.Reply(ctx, input.Message, input.Snapshot.Version, "Format: /permission 0, 1, 2, atau 3. Level 0: tanpa moderasi; 1: delete; 2: delete+mute; 3: delete+mute+kick.")
	default:
		return agent.NewError(agent.ErrorIntegrityFailure, "handle permission command", fmt.Errorf("unknown command kind"))
	}
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
