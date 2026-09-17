package commands

import (
	"context"
	"errors"
	"fmt"
	"time"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/types"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/action"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/inbound/commands/groupcmd"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/policy"
)

var GroupCommand = command.Descriptor{
	Name:        "group",
	Capability:  policy.CapabilityCommandGroup,
	Permission:  "admin and group and !fromMe",
	Description: "Mengelola status, deskripsi, pesan, mute, dan anggota grup.",
	DeniedReply: "Perintah /group hanya dapat digunakan oleh admin grup.",
	Handler:     handleGroup,
}

func handleGroup(ctx context.Context, input command.Context, rawAdapter command.Adapter) error {
	adapter, ok := rawAdapter.(WhatsAppCommandAdapter)
	if !ok || adapter == nil {
		return agent.NewError(agent.ErrorIntegrityFailure, "handle /group command", errors.New("WhatsApp command adapter is unavailable"))
	}
	send := func(text string) error {
		actionID, err := identity.NewActionID()
		if err != nil {
			return agent.NewError(agent.ErrorInternal, "create group response ID", err)
		}
		key := agent.Key{TenantID: input.Message.TenantID, AccountID: input.Message.AccountID, ChatID: input.Message.ChatID}
		if _, err := rawAdapter.SendText(ctx, action.SendTextRequest{Key: key, ActionID: actionID, Text: text}); err != nil {
			return err
		}
		return input.Store.MarkCommandHandled(ctx, input.Message)
	}
	raw := input.Message.Text
	parsed, err := groupcmd.Parse(raw)
	if err != nil {
		return send(groupCommandUsage())
	}
	var target identity.MessageID
	if parsed.Kind == groupcmd.Delete && input.Message.Quote != nil {
		target = input.Message.Quote.ID
	}
	key := agent.Key{TenantID: input.Message.TenantID, AccountID: input.Message.AccountID, ChatID: input.Message.ChatID}
	if err := HandleGroup(ctx, adapter, key, raw, target, time.Now().UTC()); err != nil {
		if agent.IsCode(err, agent.ErrorInvalidArgument) {
			return send(groupCommandUsage())
		}
		return err
	}
	return send(fmt.Sprintf("Perintah /group %s berhasil dijalankan.", parsed.Kind))
}

func groupCommandUsage() string {
	return "Format: /group close, /group open, /group description <teks>, /group delete sebagai balasan pesan, /group mute @Nama (senderRef) <menit>, atau /group kick @Nama (senderRef)."
}

type WhatsAppCommandAdapter interface {
	CommandClient() WhatsAppCommandClient
	CommandTargets() GroupTargetStore
}

type WhatsAppCommandClient interface {
	SetGroupAnnounce(context.Context, types.JID, bool) error
	SetGroupDescription(context.Context, types.JID, string) error
	BuildRevoke(types.JID, types.JID, types.MessageID) *waE2E.Message
	SendMessage(context.Context, types.JID, *waE2E.Message, ...whatsmeow.SendRequestExtra) (whatsmeow.SendResponse, error)
	UpdateGroupParticipants(context.Context, types.JID, []types.JID, whatsmeow.ParticipantChange) ([]types.GroupParticipant, error)
}

type GroupTargetStore interface {
	ResolveChatAddress(context.Context, agent.Key) (string, error)
	ResolveMessageTarget(context.Context, agent.Key, identity.MessageID) (string, string, string, time.Time, error)
	ResolveLID(context.Context, agent.Key, identity.SenderRef) (identity.LID, error)
	SetChatMute(context.Context, agent.Key, identity.SenderRef, uint32, time.Time) error
}

// HandleGroup owns parsing, target resolution, and native execution for the
// complete /group family. Successful commands have no result value.
func HandleGroup(ctx context.Context, adapter WhatsAppCommandAdapter, key agent.Key, raw string, targetMessageID identity.MessageID, now time.Time) error {
	if adapter == nil || now.IsZero() {
		return agent.NewError(agent.ErrorInvalidArgument, "handle /group command", errors.New("adapter and time are required"))
	}
	if err := key.Validate(); err != nil {
		return agent.NewError(agent.ErrorInvalidArgument, "handle /group command", err)
	}
	client, targets := adapter.CommandClient(), adapter.CommandTargets()
	if client == nil || targets == nil {
		return agent.NewError(agent.ErrorIntegrityFailure, "handle /group command", errors.New("adapter command access is unavailable"))
	}
	parsed, err := groupcmd.Parse(raw)
	if err != nil {
		return agent.NewError(agent.ErrorInvalidArgument, "handle /group command", err)
	}
	if err := parsed.ValidateTarget(targetMessageID); err != nil {
		return agent.NewError(agent.ErrorInvalidArgument, "handle /group command", err)
	}

	switch parsed.Kind {
	case groupcmd.Close, groupcmd.Open:
		chat, err := resolveGroupChat(ctx, targets, key)
		if err != nil {
			return err
		}
		return nativeGroupError(ctx, client.SetGroupAnnounce(ctx, chat, parsed.Kind == groupcmd.Close))
	case groupcmd.Description:
		chat, err := resolveGroupChat(ctx, targets, key)
		if err != nil {
			return err
		}
		return nativeGroupError(ctx, client.SetGroupDescription(ctx, chat, parsed.Description))
	case groupcmd.Delete:
		chat, providerMessageID, sender, err := resolveGroupMessage(ctx, targets, key, targetMessageID)
		if err != nil {
			return err
		}
		_, err = client.SendMessage(ctx, chat, client.BuildRevoke(chat, sender, types.MessageID(providerMessageID)))
		return nativeGroupError(ctx, err)
	case groupcmd.Mute:
		return targets.SetChatMute(ctx, key, parsed.SenderRef, parsed.Duration, now)
	case groupcmd.Kick:
		chat, err := resolveGroupChat(ctx, targets, key)
		if err != nil {
			return err
		}
		lid, err := targets.ResolveLID(ctx, key, parsed.SenderRef)
		if err != nil {
			return err
		}
		participant, err := types.ParseJID(lid.String())
		if err != nil || participant.IsEmpty() {
			return agent.NewError(agent.ErrorIntegrityFailure, "handle /group kick", errors.New("stored LID is invalid"))
		}
		results, err := client.UpdateGroupParticipants(ctx, chat, []types.JID{participant.ToNonAD()}, whatsmeow.ParticipantChangeRemove)
		if err != nil {
			return nativeGroupError(ctx, err)
		}
		if len(results) != 1 {
			return agent.NewError(agent.ErrorProviderFailure, "handle /group kick", errors.New("provider returned no participant result"))
		}
		return nil
	default:
		return agent.NewError(agent.ErrorIntegrityFailure, "handle /group command", errors.New("parsed command kind is invalid"))
	}
}

func resolveGroupChat(ctx context.Context, targets GroupTargetStore, key agent.Key) (types.JID, error) {
	address, err := targets.ResolveChatAddress(ctx, key)
	if err != nil {
		return types.EmptyJID, err
	}
	chat, err := types.ParseJID(address)
	if err != nil || chat.IsEmpty() || chat.Server != types.GroupServer {
		return types.EmptyJID, agent.NewError(agent.ErrorIntegrityFailure, "handle /group command", errors.New("stored group target is invalid"))
	}
	return chat.ToNonAD(), nil
}

func resolveGroupMessage(ctx context.Context, targets GroupTargetStore, key agent.Key, target identity.MessageID) (types.JID, string, types.JID, error) {
	address, providerMessageID, senderAddress, _, err := targets.ResolveMessageTarget(ctx, key, target)
	if err != nil {
		return types.EmptyJID, "", types.EmptyJID, err
	}
	chat, err := types.ParseJID(address)
	if err != nil || chat.IsEmpty() || chat.Server != types.GroupServer || providerMessageID == "" {
		return types.EmptyJID, "", types.EmptyJID, agent.NewError(agent.ErrorIntegrityFailure, "handle /group delete", errors.New("stored message target is invalid"))
	}
	sender := types.EmptyJID
	if senderAddress != "" {
		sender, err = types.ParseJID(senderAddress)
		if err != nil || sender.IsEmpty() {
			return types.EmptyJID, "", types.EmptyJID, agent.NewError(agent.ErrorIntegrityFailure, "handle /group delete", errors.New("stored message sender is invalid"))
		}
	}
	return chat.ToNonAD(), providerMessageID, sender.ToNonAD(), nil
}

func nativeGroupError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return agent.NewError(agent.ErrorTimeout, "handle /group command", ctx.Err())
	}
	return agent.NewError(agent.ErrorProviderFailure, "handle /group command", err)
}
