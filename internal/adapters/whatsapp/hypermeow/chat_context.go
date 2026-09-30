package hypermeow

import (
	"context"
	"errors"
	"time"

	"github.com/polymorfa/hypermeow/types"

	"github.com/Chomosuke9/DiscordAgent-Go/internal/agent"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/conversation"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/policy"
)

// ReadChatContext returns only the provider-neutral details used in the model's
// chat-information block. It does not grant authority for native effects.
func (adapter *Adapter) ReadChatContext(ctx context.Context, key agent.Key) (agent.ChatContext, error) {
	if err := key.Validate(); err != nil {
		return agent.ChatContext{}, err
	}
	if !adapter.Ready() {
		return agent.ChatContext{}, agent.NewError(agent.ErrorNotReady, "read WhatsApp chat context", errors.New("account is not connected"))
	}
	chat, err := adapter.resolveChatTarget(ctx, key)
	if err != nil {
		return agent.ChatContext{}, err
	}
	if chat.Server != types.GroupServer {
		return agent.ChatContext{Kind: "private"}, nil
	}
	info, err := adapter.readGroupInfo(ctx, chat)
	if err != nil {
		return agent.ChatContext{}, err
	}
	botLID := adapter.client.Store.GetLID().ToNonAD()
	botPhone := adapter.client.Store.GetJID().ToNonAD()
	result := agent.ChatContext{Kind: "group", Name: info.Name, Description: info.Topic}
	for _, participant := range info.Participants {
		if participantMatches(participant, botLID) || participantMatches(participant, botPhone) {
			result.BotIsAdmin = participant.IsAdmin || participant.IsSuperAdmin
			break
		}
	}
	if err := result.Validate(); err != nil {
		return agent.ChatContext{}, agent.NewError(agent.ErrorIntegrityFailure, "read WhatsApp chat context", err)
	}
	return result, nil
}

// ReadChatAuthority reads the latest synchronized group snapshot. It does not
// expose WhatsApp group DTOs across the adapter boundary or turn a model
// principal into a group participant.
func (adapter *Adapter) ReadChatAuthority(ctx context.Context, principal policy.Principal) (policy.ChatAuthority, error) {
	if err := principal.Validate(); err != nil {
		return policy.ChatAuthority{}, err
	}
	if !adapter.Ready() {
		return policy.ChatAuthority{}, agent.NewError(agent.ErrorNotReady, "read WhatsApp chat authority", errors.New("account is not connected"))
	}
	chat, err := adapter.resolveChatTarget(ctx, principal.Key())
	if err != nil {
		return policy.ChatAuthority{}, err
	}
	observedAt := time.Now().UTC().UnixMilli()
	if chat.Server != types.GroupServer {
		return policy.ChatAuthority{ChatKind: conversation.ChatDirect, ObservedAt: observedAt}, nil
	}
	info, cachedAt, err := adapter.readGroupSnapshot(ctx, chat)
	if err != nil {
		return policy.ChatAuthority{}, err
	}
	observedAt = cachedAt
	actorLID := types.EmptyJID
	if principal.Kind == policy.PrincipalHuman {
		actorLID, err = types.ParseJID(principal.LID.String())
		if err != nil || actorLID.IsEmpty() {
			return policy.ChatAuthority{}, agent.NewError(agent.ErrorIntegrityFailure, "read WhatsApp group authority", errors.New("principal LID is invalid"))
		}
	}
	botLID := adapter.client.Store.GetLID().ToNonAD()
	botPhone := adapter.client.Store.GetJID().ToNonAD()
	authority := policy.ChatAuthority{ChatKind: conversation.ChatGroup, ObservedAt: observedAt}
	for _, participant := range info.Participants {
		isAdmin := participant.IsAdmin || participant.IsSuperAdmin
		if !actorLID.IsEmpty() && participantMatches(participant, actorLID) {
			authority.ActorIsAdmin = isAdmin
		}
		if participantMatches(participant, botLID) || participantMatches(participant, botPhone) {
			authority.BotIsAdmin = isAdmin
		}
	}
	return authority, authority.Validate()
}

func (adapter *Adapter) groupRoleFlags(ctx context.Context, chat, sender types.JID) (bool, bool) {
	if chat.Server != types.GroupServer || sender.IsEmpty() {
		return false, false
	}
	info, _, err := adapter.readGroupSnapshot(ctx, chat)
	if err != nil {
		return false, false
	}
	for _, participant := range info.Participants {
		if participantMatches(participant, sender) {
			isSuperAdmin := participant.IsSuperAdmin
			return participant.IsAdmin || isSuperAdmin, isSuperAdmin
		}
	}
	return false, false
}

func participantMatches(participant types.GroupParticipant, wanted types.JID) bool {
	if wanted.IsEmpty() {
		return false
	}
	wanted = wanted.ToNonAD()
	for _, candidate := range []types.JID{participant.JID, participant.LID, participant.PhoneNumber} {
		if !candidate.IsEmpty() && candidate.ToNonAD() == wanted {
			return true
		}
	}
	return false
}
