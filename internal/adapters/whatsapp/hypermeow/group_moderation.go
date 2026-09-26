package hypermeow

import (
	"context"
	"errors"
	"time"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/types"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

// The methods below implement the /group moderation port. They resolve agent
// identities to WhatsApp addresses so no whatsmeow type crosses the boundary.

func (adapter *Adapter) SetGroupAnnounce(ctx context.Context, key agent.Key, announce bool) error {
	chat, err := adapter.resolveGroupChat(ctx, key)
	if err != nil {
		return err
	}
	return groupModerationError(ctx, "set WhatsApp group announce", adapter.client.SetGroupAnnounce(ctx, chat, announce))
}

func (adapter *Adapter) SetGroupDescription(ctx context.Context, key agent.Key, description string) error {
	chat, err := adapter.resolveGroupChat(ctx, key)
	if err != nil {
		return err
	}
	return groupModerationError(ctx, "set WhatsApp group description", adapter.client.SetGroupDescription(ctx, chat, description))
}

func (adapter *Adapter) RevokeGroupMessage(ctx context.Context, key agent.Key, target identity.MessageID) error {
	if !adapter.Ready() {
		return agent.NewError(agent.ErrorNotReady, "revoke WhatsApp group message", errors.New("account is not connected"))
	}
	address, providerMessageID, senderAddress, _, err := adapter.targets.ResolveMessageTarget(ctx, key, target)
	if err != nil {
		return err
	}
	chat, err := types.ParseJID(address)
	if err != nil || chat.IsEmpty() || chat.Server != types.GroupServer || providerMessageID == "" {
		return agent.NewError(agent.ErrorIntegrityFailure, "revoke WhatsApp group message", errors.New("stored message target is invalid"))
	}
	sender := types.EmptyJID
	if senderAddress != "" {
		sender, err = types.ParseJID(senderAddress)
		if err != nil || sender.IsEmpty() {
			return agent.NewError(agent.ErrorIntegrityFailure, "revoke WhatsApp group message", errors.New("stored message sender is invalid"))
		}
	}
	chat, sender = chat.ToNonAD(), sender.ToNonAD()
	_, err = adapter.client.SendMessage(ctx, chat, adapter.client.BuildRevoke(chat, sender, types.MessageID(providerMessageID)))
	return groupModerationError(ctx, "revoke WhatsApp group message", err)
}

func (adapter *Adapter) RemoveGroupMember(ctx context.Context, key agent.Key, ref identity.SenderRef) error {
	chat, err := adapter.resolveGroupChat(ctx, key)
	if err != nil {
		return err
	}
	lid, err := adapter.targets.ResolveLID(ctx, key, ref)
	if err != nil {
		return err
	}
	participant, err := types.ParseJID(lid.String())
	if err != nil || participant.IsEmpty() {
		return agent.NewError(agent.ErrorIntegrityFailure, "remove WhatsApp group member", errors.New("stored LID is invalid"))
	}
	results, err := adapter.client.UpdateGroupParticipants(ctx, chat, []types.JID{participant.ToNonAD()}, whatsmeow.ParticipantChangeRemove)
	if err != nil {
		return groupModerationError(ctx, "remove WhatsApp group member", err)
	}
	if len(results) != 1 {
		return agent.NewError(agent.ErrorProviderFailure, "remove WhatsApp group member", errors.New("provider returned no participant result"))
	}
	return nil
}

// MuteGroupMember is enforced locally by dropping the member's messages, so it
// needs neither a connection nor a provider call.
func (adapter *Adapter) MuteGroupMember(ctx context.Context, key agent.Key, ref identity.SenderRef, minutes uint32, now time.Time) error {
	return adapter.targets.SetChatMute(ctx, key, ref, minutes, now)
}

func (adapter *Adapter) resolveGroupChat(ctx context.Context, key agent.Key) (types.JID, error) {
	if !adapter.Ready() {
		return types.EmptyJID, agent.NewError(agent.ErrorNotReady, "resolve WhatsApp group", errors.New("account is not connected"))
	}
	chat, err := adapter.resolveChatTarget(ctx, key)
	if err != nil {
		return types.EmptyJID, err
	}
	if chat.Server != types.GroupServer {
		return types.EmptyJID, agent.NewError(agent.ErrorIntegrityFailure, "resolve WhatsApp group", errors.New("stored group target is invalid"))
	}
	return chat, nil
}

func groupModerationError(ctx context.Context, operation string, err error) error {
	if err == nil {
		return nil
	}
	return nativeEffectError(ctx, operation, err)
}
