package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
)

func init() {
	register(command.Command{
		Name:        "catch",
		Permission:  "public",
		Description: "Outputs the raw JSON payload of the replied-to WhatsApp message.",
		Run:         runCatch,
	})
}

func runCatch(ctx context.Context, c *command.Context) error {
	if c.HasArgs {
		return c.Reply(ctx, "Usage: reply to a WhatsApp message with /catch.")
	}
	quoted, err := c.QuotedRaw(ctx)
	if agent.IsCode(err, agent.ErrorNotFound) {
		return c.Reply(ctx, "Reply to a WhatsApp message with /catch. Its raw payload or sender identity is unavailable.")
	}
	if err != nil {
		return err
	}
	response, err := formatCaughtMessage(quoted)
	if err != nil {
		return agent.NewError(agent.ErrorIntegrityFailure, "format /catch response", err)
	}
	return c.Reply(ctx, response)
}

func formatCaughtMessage(message command.RawQuotedMessage) (string, error) {
	if message.ProviderMessageID == "" || message.RemoteJID == "" || !json.Valid(message.MessageJSON) ||
		!strings.HasPrefix(strings.TrimSpace(string(message.MessageJSON)), "{") {
		return "", fmt.Errorf("captured provider message is invalid")
	}
	response := struct {
		Key struct {
			ID        string `json:"id"`
			RemoteJID string `json:"remoteJid"`
			FromMe    bool   `json:"fromMe"`
		} `json:"key"`
		Message json.RawMessage `json:"message"`
	}{}
	response.Key.ID = message.ProviderMessageID
	response.Key.RemoteJID = message.RemoteJID
	response.Key.FromMe = message.FromMe
	response.Message = json.RawMessage(message.MessageJSON)
	encoded, err := json.MarshalIndent(response, "", "  ")
	if err != nil {
		return "", err
	}
	return "```json\n" + string(encoded) + "\n```", nil
}
