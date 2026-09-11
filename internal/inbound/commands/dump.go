package commands

import (
	"context"
	"fmt"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/policy"
)

var DumpCommand = command.Descriptor{
	Name:        "dump",
	Capability:  policy.CapabilityChatContextRead,
	Permission:  "owner and !fromMe",
	Description: "Menampilkan context model yang benar-benar dibangun agent.",
	DeniedReply: "Perintah /dump hanya dapat digunakan oleh owner yang dikonfigurasi.",
	Handler:     handleDump,
}

func handleDump(ctx context.Context, request command.Request, input command.Context) error {
	if request.ArgumentsPresent {
		return input.Responses.Reply(ctx, input.Message, input.Snapshot.Version,
			fmt.Sprintf("Format perintah /%s tidak menerima argumen.", request.Name))
	}
	modelInput, err := input.Agent.BuildInput(ctx, input.Snapshot.Version, input.Message.InvocationID)
	if err != nil {
		return err
	}
	return input.Responses.Reply(ctx, input.Message, input.Snapshot.Version, agent.SerializeModelMessages(modelInput))
}
