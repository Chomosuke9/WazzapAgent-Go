package commands

import (
	"context"
	"fmt"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/policy"
)

var PromptCommand = command.Descriptor{
	Name:        "prompt",
	Capability:  policy.CapabilityPromptWrite,
	Permission:  "owner and !fromMe",
	Description: "Melihat, mengubah, atau menghapus prompt custom chat.",
	DeniedReply: "Perintah /prompt hanya dapat digunakan oleh owner yang dikonfigurasi.",
	Handler:     handlePrompt,
}

func handlePrompt(ctx context.Context, request command.Request, input command.Context) error {
	parsed, recognized := command.ParsePromptCommand(command.CanonicalText(request))
	if !recognized {
		return agent.NewError(agent.ErrorIntegrityFailure, "handle prompt command", fmt.Errorf("prompt command was not parsed"))
	}
	var response string
	snapshot := input.Snapshot
	switch parsed.Kind {
	case command.PromptView:
		if snapshot.PromptOverride == nil {
			response = "Prompt override belum diatur."
		} else {
			response = "Prompt override saat ini:\n" + snapshot.PromptOverride.Text
		}
	case command.PromptSet:
		updated, err := applyPromptMutation(ctx, input, parsed)
		if err != nil {
			return err
		}
		snapshot.Version = updated
		response = "Prompt override berhasil diperbarui."
	case command.PromptClear:
		updated, err := applyPromptMutation(ctx, input, parsed)
		if err != nil {
			return err
		}
		snapshot.Version = updated
		response = "Prompt override berhasil dihapus."
	case command.PromptInvalid:
		response = fmt.Sprintf("Format: /prompt view, /prompt set <teks>, atau /prompt clear. Panjang prompt maksimal %d byte.", agent.MaxPromptBytes)
	default:
		return agent.NewError(agent.ErrorIntegrityFailure, "handle prompt command", fmt.Errorf("unknown command kind"))
	}
	return input.Responses.Reply(ctx, input.Message, snapshot.Version, response)
}

func applyPromptMutation(ctx context.Context, input command.Context, parsed command.PromptCommand) (agent.ConfigVersion, error) {
	journal, err := input.Store.BeginPromptMutation(ctx, input.Message, parsed, input.Snapshot.Version)
	if err != nil {
		return 0, err
	}
	if journal.AppliedVersion != 0 {
		return journal.AppliedVersion, nil
	}
	if input.Snapshot.Version == journal.ExpectedVersion+1 && promptMutationMatches(input.Snapshot, parsed) {
		if err := input.Store.MarkPromptMutationApplied(ctx, input.Message, journal.ExpectedVersion, input.Snapshot.Version); err != nil {
			return 0, err
		}
		return input.Snapshot.Version, nil
	}
	if input.Snapshot.Version != journal.ExpectedVersion {
		return 0, agent.NewError(agent.ErrorConflict, "apply prompt mutation", fmt.Errorf("config changed after command authorization; resend the command"))
	}
	var updated agent.ConfigSnapshot
	switch parsed.Kind {
	case command.PromptSet:
		updated, err = input.Agent.Config().SetPromptOverride(ctx, input.Snapshot.Version, agent.PromptOverride{Mode: agent.PromptAppend, Text: parsed.Text})
	case command.PromptClear:
		updated, err = input.Agent.Config().ClearPromptOverride(ctx, input.Snapshot.Version)
	default:
		return 0, agent.NewError(agent.ErrorInvalidArgument, "apply prompt mutation", fmt.Errorf("command is not a mutation"))
	}
	if err != nil {
		return 0, err
	}
	if err := input.Store.MarkPromptMutationApplied(ctx, input.Message, journal.ExpectedVersion, updated.Version); err != nil {
		return 0, err
	}
	return updated.Version, nil
}

func promptMutationMatches(snapshot agent.ConfigSnapshot, parsed command.PromptCommand) bool {
	switch parsed.Kind {
	case command.PromptSet:
		return snapshot.PromptOverride != nil && snapshot.PromptOverride.Mode == agent.PromptAppend && snapshot.PromptOverride.Text == parsed.Text
	case command.PromptClear:
		return snapshot.PromptOverride == nil
	default:
		return false
	}
}
