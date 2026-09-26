package commands

import (
	"context"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
)

func init() {
	register(command.Command{
		Name:        "dump",
		Permission:  "owner and !fromMe",
		Description: "Shows the model context actually built by the Agent.",
		DeniedReply: "The /dump command can only be used by the configured owner.",
		Run:         runDump,
	})
}

func runDump(ctx context.Context, c *command.Context) error {
	if c.HasArgs {
		return c.Reply(ctx, "The /dump command does not accept arguments.")
	}
	input, err := c.Agent.BuildInput(ctx, c.Config.Version, c.Message.InvocationID)
	if err != nil {
		return err
	}
	return c.Reply(ctx, agent.SerializeModelMessages(input))
}
