package commands

import (
	"context"
	"fmt"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/policy"
)

var InfoCommand = command.Descriptor{
	Name:        "info",
	Capability:  policy.CapabilityCommandInfo,
	Permission:  "public",
	Description: "Menampilkan status agent, model, config, dan history.",
	Handler:     handleInfo,
}

func handleInfo(ctx context.Context, request command.Request, input command.Context, adapter any) error {
	if request.ArgumentsPresent {
		return input.Responses.Reply(ctx, input.Message, input.Snapshot.Version,
			fmt.Sprintf("Format perintah /%s tidak menerima argumen.", request.Name))
	}
	page, err := input.Agent.History().List(ctx, input.Snapshot.Version, agent.HistoryQuery{Limit: 1})
	if err != nil {
		return err
	}
	historyState := "kosong"
	if len(page.Entries) > 0 {
		historyState = "aktif"
	}
	response := fmt.Sprintf("Agent aktif. Model: %s. Config version: %d. History: %s.", input.Snapshot.Model.Model, input.Snapshot.Version, historyState)
	return input.Responses.Reply(ctx, input.Message, input.Snapshot.Version, response)
}
