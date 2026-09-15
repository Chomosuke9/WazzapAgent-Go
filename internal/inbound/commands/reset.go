package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/policy"
)

var ResetCommand = command.Descriptor{
	Name:        "reset",
	Capability:  policy.CapabilityHistoryReset,
	Permission:  "owner and !fromMe",
	Description: "Menghapus history percakapan chat ini.",
	DeniedReply: "Perintah /reset hanya dapat digunakan oleh owner yang dikonfigurasi.",
	Handler:     handleReset,
}

func handleReset(ctx context.Context, input command.Context, adapter any) error {
	token, _, argumentsPresent := strings.Cut(strings.TrimPrefix(input.Message.Text, "/"), " ")
	if argumentsPresent {
		return sendText(ctx, input, adapter,
			fmt.Sprintf("Format perintah /%s tidak menerima argumen.", token))
	}
	if err := input.Agent.History().Reset(ctx, input.Snapshot.Version); err != nil {
		return err
	}
	input.Observer.ObserveHistoryReset()
	return sendText(ctx, input, adapter, "History percakapan berhasil direset.")
}
