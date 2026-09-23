package hypermeow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/types"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

const memberHandleTTL = 10 * time.Minute

// GroupMember is the provider-neutral subset displayed by the local desktop.
// ID is an ephemeral handle and cannot be interpreted as a WhatsApp address.
type GroupMember struct {
	ID           string
	Name         string
	IsAdmin      bool
	IsSuperAdmin bool
	CanKick      bool
}

type GroupMembers struct {
	BotIsAdmin bool
	Members    []GroupMember
}

type memberHandleSet struct {
	expiresAt time.Time
	addresses map[string]types.JID
}

func (adapter *Adapter) ListGroupMembers(ctx context.Context, key agent.Key) (GroupMembers, error) {
	if err := key.Validate(); err != nil {
		return GroupMembers{}, err
	}
	if !adapter.Ready() {
		return GroupMembers{}, agent.NewError(agent.ErrorNotReady, "list WhatsApp group members", errors.New("account is not connected"))
	}
	chat, err := adapter.resolveChatTarget(ctx, key)
	if err != nil {
		return GroupMembers{}, err
	}
	if chat.Server != types.GroupServer {
		return GroupMembers{}, agent.NewError(agent.ErrorInvalidArgument, "list WhatsApp group members", errors.New("selected conversation is not a group"))
	}
	readCtx, cancel := context.WithTimeout(ctx, adapter.sendTimeout)
	defer cancel()
	info, err := adapter.client.GetGroupInfo(readCtx, chat)
	if err != nil {
		return GroupMembers{}, nativeEffectError(readCtx, "list WhatsApp group members", err)
	}
	adapter.cacheGroupName(ctx, chat, info.Name)

	botLID := adapter.client.Store.GetLID().ToNonAD()
	botPhone := adapter.client.Store.GetJID().ToNonAD()
	addresses := make(map[string]types.JID, len(info.Participants))
	members := make([]GroupMember, 0, len(info.Participants))
	botIsAdmin := false
	for _, participant := range info.Participants {
		if (participantMatches(participant, botLID) || participantMatches(participant, botPhone)) && (participant.IsAdmin || participant.IsSuperAdmin) {
			botIsAdmin = true
			break
		}
	}
	for index, participant := range info.Participants {
		address := participantAddress(participant)
		if address.IsEmpty() {
			continue
		}
		handle, err := identity.NewParticipantID()
		if err != nil {
			return GroupMembers{}, agent.NewError(agent.ErrorInternal, "create WhatsApp member handle", errors.New("could not allocate a member handle"))
		}
		isBot := participantMatches(participant, botLID) || participantMatches(participant, botPhone)
		isAdmin := participant.IsAdmin || participant.IsSuperAdmin
		members = append(members, GroupMember{
			ID: handle.String(), Name: adapter.groupMemberName(readCtx, participant, index+1),
			IsAdmin: participant.IsAdmin, IsSuperAdmin: participant.IsSuperAdmin,
			CanKick: botIsAdmin && !isBot && !isAdmin,
		})
		addresses[handle.String()] = address.ToNonAD()
	}

	adapter.memberHandlesMu.Lock()
	now := time.Now()
	for chatID, handles := range adapter.memberHandles {
		if !handles.expiresAt.After(now) {
			delete(adapter.memberHandles, chatID)
		}
	}
	adapter.memberHandles[key.ChatID.String()] = memberHandleSet{expiresAt: now.Add(memberHandleTTL), addresses: addresses}
	adapter.memberHandlesMu.Unlock()
	return GroupMembers{BotIsAdmin: botIsAdmin, Members: members}, nil
}

func (adapter *Adapter) KickGroupMember(ctx context.Context, key agent.Key, memberID string) error {
	if err := key.Validate(); err != nil {
		return err
	}
	handle, err := identity.ParseParticipantID(memberID)
	if err != nil {
		return agent.NewError(agent.ErrorInvalidArgument, "kick WhatsApp group member", errors.New("member handle is invalid"))
	}
	if !adapter.Ready() {
		return agent.NewError(agent.ErrorNotReady, "kick WhatsApp group member", errors.New("account is not connected"))
	}

	adapter.memberHandlesMu.Lock()
	handles, ok := adapter.memberHandles[key.ChatID.String()]
	memberAddress := handles.addresses[handle.String()]
	validHandle := ok && handles.expiresAt.After(time.Now()) && !memberAddress.IsEmpty()
	adapter.memberHandlesMu.Unlock()
	if !validHandle {
		return agent.NewError(agent.ErrorNotFound, "kick WhatsApp group member", errors.New("refresh the group member list before trying again"))
	}

	chat, err := adapter.resolveChatTarget(ctx, key)
	if err != nil {
		return err
	}
	if chat.Server != types.GroupServer {
		return agent.NewError(agent.ErrorInvalidArgument, "kick WhatsApp group member", errors.New("selected conversation is not a group"))
	}
	requestCtx, cancel := context.WithTimeout(ctx, adapter.sendTimeout)
	defer cancel()
	info, err := adapter.client.GetGroupInfo(requestCtx, chat)
	if err != nil {
		return nativeEffectError(requestCtx, "verify WhatsApp group member", err)
	}
	adapter.cacheGroupName(ctx, chat, info.Name)
	botLID := adapter.client.Store.GetLID().ToNonAD()
	botPhone := adapter.client.Store.GetJID().ToNonAD()
	botIsAdmin := false
	var current *types.GroupParticipant
	for index := range info.Participants {
		participant := &info.Participants[index]
		if participantMatches(*participant, botLID) || participantMatches(*participant, botPhone) {
			botIsAdmin = participant.IsAdmin || participant.IsSuperAdmin
		}
		if participantMatches(*participant, memberAddress) {
			current = participant
		}
	}
	if !botIsAdmin {
		return agent.NewError(agent.ErrorPermissionDenied, "kick WhatsApp group member", errors.New("the connected WhatsApp account is not a current group admin"))
	}
	if current == nil {
		return agent.NewError(agent.ErrorNotFound, "kick WhatsApp group member", errors.New("member is no longer in this group"))
	}
	if participantMatches(*current, botLID) || participantMatches(*current, botPhone) || current.IsAdmin || current.IsSuperAdmin {
		return agent.NewError(agent.ErrorPermissionDenied, "kick WhatsApp group member", errors.New("this group member cannot be removed from the desktop"))
	}

	stripe := adapter.sendStripe(key.ChatID.String())
	stripe.Lock()
	defer stripe.Unlock()
	results, err := adapter.client.UpdateGroupParticipants(requestCtx, chat, []types.JID{memberAddress}, whatsmeow.ParticipantChangeRemove)
	if err != nil {
		return nativeEffectError(requestCtx, "kick WhatsApp group member", err)
	}
	if len(results) != 1 || results[0].Error != 0 {
		return agent.NewError(agent.ErrorProviderFailure, "kick WhatsApp group member", errors.New("WhatsApp did not confirm the member removal"))
	}
	adapter.memberHandlesMu.Lock()
	if currentSet, exists := adapter.memberHandles[key.ChatID.String()]; exists {
		delete(currentSet.addresses, handle.String())
		adapter.memberHandles[key.ChatID.String()] = currentSet
	}
	adapter.memberHandlesMu.Unlock()
	return nil
}

func (adapter *Adapter) authorizeMessageDeletion(ctx context.Context, chat, sender types.JID) error {
	if sender.IsEmpty() {
		return nil
	}
	sender = sender.ToNonAD()
	botLID := adapter.client.Store.GetLID().ToNonAD()
	botPhone := adapter.client.Store.GetJID().ToNonAD()
	if sender == botLID || sender == botPhone {
		return nil
	}
	if chat.Server != types.GroupServer {
		return agent.NewError(agent.ErrorPermissionDenied, "authorize WhatsApp message deletion", errors.New("messages from other people can only be removed by an admin in a group"))
	}
	info, err := adapter.client.GetGroupInfo(ctx, chat)
	if err != nil {
		return nativeEffectError(ctx, "verify group admin before deleting WhatsApp message", err)
	}
	adapter.cacheGroupName(ctx, chat, info.Name)
	for _, participant := range info.Participants {
		if (participantMatches(participant, botLID) || participantMatches(participant, botPhone)) && (participant.IsAdmin || participant.IsSuperAdmin) {
			return nil
		}
	}
	return agent.NewError(agent.ErrorPermissionDenied, "authorize WhatsApp message deletion", errors.New("the connected WhatsApp account is not a current group admin"))
}

func (adapter *Adapter) groupMemberName(ctx context.Context, participant types.GroupParticipant, ordinal int) string {
	if name := strings.TrimSpace(participant.DisplayName); name != "" {
		return name
	}
	for _, address := range []types.JID{participant.JID, participant.PhoneNumber, participant.LID} {
		if address.IsEmpty() || adapter.client.Store == nil || adapter.client.Store.Contacts == nil {
			continue
		}
		contact, err := adapter.client.Store.Contacts.GetContact(ctx, address)
		if err != nil || !contact.Found {
			continue
		}
		for _, name := range []string{contact.FullName, contact.FirstName, contact.PushName, contact.BusinessName, contact.Username} {
			if name = strings.TrimSpace(name); name != "" {
				return name
			}
		}
	}
	return fmt.Sprintf("Member %d", ordinal)
}

func participantAddress(participant types.GroupParticipant) types.JID {
	for _, address := range []types.JID{participant.JID, participant.LID, participant.PhoneNumber} {
		if !address.IsEmpty() {
			return address
		}
	}
	return types.EmptyJID
}

func (adapter *Adapter) clearMemberHandles() {
	adapter.memberHandlesMu.Lock()
	adapter.memberHandles = make(map[string]memberHandleSet)
	adapter.memberHandlesMu.Unlock()
}
