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

func handleHelp(ctx context.Context, input command.Context, adapter any) error {
	token, _, argumentsPresent := strings.Cut(strings.TrimPrefix(input.Message.Text, "/"), " ")
	if argumentsPresent {
		return sendText(ctx, input, adapter,
			fmt.Sprintf("Format perintah /%s tidak menerima argumen.", token))
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
	return sendText(ctx, input, adapter, strings.Join(lines, "\n"))
}
