package commands

import (
	"context"
	"fmt"
	"strings"

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

func handleDump(ctx context.Context, input command.Context, adapter any) error {
	token, _, argumentsPresent := strings.Cut(strings.TrimPrefix(input.Message.Text, "/"), " ")
	if argumentsPresent {
		return sendText(ctx, input, adapter,
			fmt.Sprintf("Format perintah /%s tidak menerima argumen.", token))
	}
	modelInput, err := input.Agent.BuildInput(ctx, input.Snapshot.Version, input.Message.InvocationID)
	if err != nil {
		return err
	}
	return sendText(ctx, input, adapter, agent.SerializeModelMessages(modelInput))
}
