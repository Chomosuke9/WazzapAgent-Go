package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/policy"
)

var HelpCommand = command.Descriptor{
	Name:        "help",
	Aliases:     []string{"menu"},
	Capability:  policy.CapabilityCommandHelp,
	Permission:  "public",
	Description: "Menampilkan daftar command yang tersedia.",
	Handler:     handleHelp,
}

func handleHelp(ctx context.Context, request command.Request, input command.Context) error {
	if request.ArgumentsPresent {
		return input.Responses.Reply(ctx, input.Message, input.Snapshot.Version,
			fmt.Sprintf("Format perintah /%s tidak menerima argumen.", request.Name))
	}
	if input.Registry == nil {
		return agent.NewError(agent.ErrorIntegrityFailure, "handle help command", fmt.Errorf("command registry is required"))
	}
	descriptors := input.Registry.Descriptors()
	lines := []string{"Perintah yang tersedia:"}
	for _, descriptor := range descriptors {
		aliases := ""
		if len(descriptor.Aliases) > 0 {
			aliases = " (alias: /" + strings.Join(descriptor.Aliases, ", /") + ")"
		}
		lines = append(lines, fmt.Sprintf("/%s%s - %s", descriptor.Name, aliases, descriptor.Description))
	}
	return input.Responses.Reply(ctx, input.Message, input.Snapshot.Version, strings.Join(lines, "\n"))
}
