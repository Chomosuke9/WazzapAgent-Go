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

var DumpCommand = command.Descriptor{
	Name:        "dump",
	Capability:  policy.CapabilityChatContextRead,
	Permission:  "owner and !fromMe",
	Description: "Menampilkan context model yang benar-benar dibangun agent.",
	DeniedReply: "Perintah /dump hanya dapat digunakan oleh owner yang dikonfigurasi.",
	Handler:     handleDump,
}

func handleDump(ctx context.Context, input command.Context, adapter command.Adapter) error {
	send := func(text string) error {
		actionID, err := identity.NewActionID()
		if err != nil {
			return agent.NewError(agent.ErrorInternal, "create dump response ID", err)
		}
		key := agent.Key{TenantID: input.Message.TenantID, AccountID: input.Message.AccountID, ChatID: input.Message.ChatID}
		if _, err := adapter.SendText(ctx, action.SendTextRequest{Key: key, ActionID: actionID, Text: text}); err != nil {
			return err
		}
		return input.Store.MarkCommandHandled(ctx, input.Message)
	}
	token, _, argumentsPresent := strings.Cut(strings.TrimPrefix(input.Message.Text, "/"), " ")
	if argumentsPresent {
		return send(fmt.Sprintf("Format perintah /%s tidak menerima argumen.", token))
	}
	modelInput, err := input.Agent.BuildInput(ctx, input.Snapshot.Version, input.Message.InvocationID)
	if err != nil {
		return err
	}
	return send(agent.SerializeModelMessages(modelInput))
}
