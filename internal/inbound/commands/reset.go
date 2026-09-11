package commands

import (
	"context"
	"fmt"

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

func handleReset(ctx context.Context, request command.Request, input command.Context) error {
	if request.ArgumentsPresent {
		return input.Responses.Reply(ctx, input.Message, input.Snapshot.Version,
			fmt.Sprintf("Format perintah /%s tidak menerima argumen.", request.Name))
	}
	if err := input.Agent.History().Reset(ctx, input.Snapshot.Version); err != nil {
		return err
	}
	response := "History percakapan berhasil direset."
	// The confirmation itself is part of the full transcript, but it must not
	// immediately repopulate the new conversation context.
	replyErr := input.Responses.Reply(ctx, input.Message, input.Snapshot.Version, response)
	resetErr := input.Agent.History().Reset(ctx, input.Snapshot.Version)
	input.Observer.ObserveHistoryReset()
	if replyErr != nil {
		return replyErr
	}
	return resetErr
}
