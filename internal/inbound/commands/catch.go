package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/action"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/policy"
)

var CatchCommand = command.Descriptor{
	Name:        "catch",
	Capability:  policy.CapabilityCommandCatch,
	Permission:  "public",
	Description: "Outputs the raw JSON payload of the replied-to WhatsApp message.",
	Handler:     handleCatch,
}

func handleCatch(ctx context.Context, input command.Context, adapter command.Adapter) error {
	send := func(text string) error {
		actionID, err := identity.NewActionID()
		if err != nil {
			return agent.NewError(agent.ErrorInternal, "create /catch response ID", err)
		}
		key := agent.Key{TenantID: input.Message.TenantID, AccountID: input.Message.AccountID, ChatID: input.Message.ChatID}
		if _, err := adapter.SendText(ctx, action.SendTextRequest{Key: key, ActionID: actionID, Text: text}); err != nil {
			return err
		}
		return input.Store.MarkCommandHandled(ctx, input.Message)
	}

	_, _, argumentsPresent := strings.Cut(strings.TrimPrefix(input.Message.Text, "/"), " ")
	if argumentsPresent {
		return send("Usage: reply to a WhatsApp message with /catch.")
	}
	reader, ok := input.Store.(command.RawQuotedMessageReader)
	if !ok {
		return agent.NewError(agent.ErrorIntegrityFailure, "handle /catch command", errors.New("raw quoted message reader is unavailable"))
	}
	quoted, err := reader.ReadRawQuotedMessage(ctx, input.Message)
	if agent.IsCode(err, agent.ErrorNotFound) {
		return send("Reply to a WhatsApp message with /catch. Its raw payload or sender identity is unavailable.")
	}
	if err != nil {
		return err
	}
	response, err := formatCaughtMessage(quoted)
	if err != nil {
		return agent.NewError(agent.ErrorIntegrityFailure, "format /catch response", err)
	}
	return send(response)
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
