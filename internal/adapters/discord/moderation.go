package discord

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/Chomosuke9/DiscordAgent-Go/internal/agent"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/identity"
)

const (
	memberHandleTTL = 10 * time.Minute
	maxListMembers  = 1000
	maxTopicRunes   = 1024
)

// The methods below implement the /mod moderation port. They resolve core
// identities to Discord IDs so no discordgo type crosses the boundary.

// SetGroupAnnounce locks the channel so only members with a role that may
// still send can talk, or unlocks it again. A thread is locked instead.
func (adapter *Adapter) SetGroupAnnounce(ctx context.Context, key agent.Key, announce bool) error {
	channel, err := adapter.moderatedChannel(ctx, key)
	if err != nil {
		return err
	}
	requestCtx, cancel := context.WithTimeout(ctx, adapter.sendTimeout)
	defer cancel()
	if channel.IsThread() {
		if err := adapter.requirePermission(ctx, channel, discordgo.PermissionManageThreads, "Manage Threads"); err != nil {
			return err
		}
		if _, err := adapter.client.ChannelEdit(channel.ID, &discordgo.ChannelEdit{Locked: &announce}, discordgo.WithContext(requestCtx)); err != nil {
			return providerError(requestCtx, "lock Discord thread", err)
		}
		return nil
	}
	if err := adapter.requirePermission(ctx, channel, discordgo.PermissionManageRoles, "Manage Permissions"); err != nil {
		return err
	}
	var allow, deny int64
	for _, overwrite := range channel.PermissionOverwrites {
		if overwrite != nil && overwrite.Type == discordgo.PermissionOverwriteTypeRole && overwrite.ID == channel.GuildID {
			allow, deny = overwrite.Allow, overwrite.Deny
		}
	}
	const sending = discordgo.PermissionSendMessages | discordgo.PermissionSendMessagesInThreads
	if announce {
		allow &^= sending
		deny |= sending
	} else {
		deny &^= sending
	}
	if err := adapter.client.ChannelPermissionSet(channel.ID, channel.GuildID, discordgo.PermissionOverwriteTypeRole, allow, deny, discordgo.WithContext(requestCtx)); err != nil {
		return providerError(requestCtx, "change Discord channel permissions", err)
	}
	return nil
}

// SetGroupDescription sets the channel topic.
func (adapter *Adapter) SetGroupDescription(ctx context.Context, key agent.Key, description string) error {
	channel, err := adapter.moderatedChannel(ctx, key)
	if err != nil {
		return err
	}
	if channel.IsThread() {
		return agent.NewError(agent.ErrorUnsupported, "set Discord channel topic", errors.New("threads have no topic"))
	}
	if len([]rune(description)) > maxTopicRunes {
		return agent.NewError(agent.ErrorInvalidArgument, "set Discord channel topic", fmt.Errorf("a channel topic holds at most %d characters", maxTopicRunes))
	}
	if err := adapter.requirePermission(ctx, channel, discordgo.PermissionManageChannels, "Manage Channel"); err != nil {
		return err
	}
	requestCtx, cancel := context.WithTimeout(ctx, adapter.sendTimeout)
	defer cancel()
	if _, err := adapter.client.ChannelEdit(channel.ID, &discordgo.ChannelEdit{Topic: description}, discordgo.WithContext(requestCtx)); err != nil {
		return providerError(requestCtx, "set Discord channel topic", err)
	}
	return nil
}

// RevokeGroupMessage deletes a stored message for everyone.
func (adapter *Adapter) RevokeGroupMessage(ctx context.Context, key agent.Key, target identity.MessageID) error {
	if _, err := adapter.moderatedChannel(ctx, key); err != nil {
		return err
	}
	channel, providerMessageID, sender, err := adapter.resolveEffectTarget(ctx, key, target)
	if err != nil {
		return err
	}
	return adapter.deleteMessage(ctx, channel, providerMessageID, sender)
}

// RemoveGroupMember kicks the member ref names from the server.
func (adapter *Adapter) RemoveGroupMember(ctx context.Context, key agent.Key, ref identity.SenderRef) error {
	channel, err := adapter.moderatedChannel(ctx, key)
	if err != nil {
		return err
	}
	userID, err := adapter.targets.ResolveUserID(ctx, key, ref)
	if err != nil {
		return err
	}
	return adapter.kick(ctx, channel, userID.String())
}

// MuteGroupMember is enforced locally by deleting the member's messages, so
// it needs neither a connection nor a Discord call.
func (adapter *Adapter) MuteGroupMember(ctx context.Context, key agent.Key, ref identity.SenderRef, minutes uint32, now time.Time) error {
	return adapter.targets.SetChatMute(ctx, key, ref, minutes, now)
}

func (adapter *Adapter) kick(ctx context.Context, channel *discordgo.Channel, userID string) error {
	if userID == adapter.currentBotID() {
		return agent.NewError(agent.ErrorPermissionDenied, "kick Discord member", errors.New("the bot can not kick itself"))
	}
	if err := adapter.requirePermission(ctx, channel, discordgo.PermissionKickMembers, "Kick Members"); err != nil {
		return err
	}
	guild, err := adapter.guild(ctx, channel.GuildID)
	if err != nil {
		return err
	}
	if guild.OwnerID == userID {
		return agent.NewError(agent.ErrorPermissionDenied, "kick Discord member", errors.New("the server owner can not be kicked"))
	}
	requestCtx, cancel := context.WithTimeout(ctx, adapter.sendTimeout)
	defer cancel()
	if err := adapter.client.GuildMemberDeleteWithReason(guild.ID, userID, "Removed by the Agent", discordgo.WithContext(requestCtx)); err != nil {
		return providerError(requestCtx, "kick Discord member", err)
	}
	return nil
}

func (adapter *Adapter) moderatedChannel(ctx context.Context, key agent.Key) (*discordgo.Channel, error) {
	if !adapter.Ready() {
		return nil, agent.NewError(agent.ErrorNotReady, "moderate Discord channel", errors.New("bot is not connected"))
	}
	channel, err := adapter.chatChannel(ctx, key)
	if err != nil {
		return nil, err
	}
	if isDirect(channel) {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "moderate Discord channel", errors.New("moderation works only in server channels"))
	}
	return channel, nil
}

func (adapter *Adapter) requirePermission(ctx context.Context, channel *discordgo.Channel, permission int64, name string) error {
	permissions, err := adapter.botPermissions(ctx, channel)
	if err != nil {
		return err
	}
	if permissions&permission == 0 {
		return agent.NewError(agent.ErrorPermissionDenied, "check Discord permission", fmt.Errorf("the bot needs the %s permission here", name))
	}
	return nil
}

// GroupMember is the provider-neutral subset the app shows for a channel.
// ID is a short-lived handle, never a Discord user ID.
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
	users     map[string]string
}

// ListGroupMembers lists the server members who can see the channel. It
// needs the Server Members intent switched on in the Developer Portal.
func (adapter *Adapter) ListGroupMembers(ctx context.Context, key agent.Key) (GroupMembers, error) {
	if err := key.Validate(); err != nil {
		return GroupMembers{}, err
	}
	channel, err := adapter.moderatedChannel(ctx, key)
	if err != nil {
		return GroupMembers{}, err
	}
	guild, err := adapter.guild(ctx, channel.GuildID)
	if err != nil {
		return GroupMembers{}, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, adapter.sendTimeout)
	members, err := adapter.client.GuildMembers(guild.ID, "", maxListMembers, discordgo.WithContext(requestCtx))
	if err != nil {
		err = providerError(requestCtx, "list Discord members", err)
		cancel()
		if agent.IsCode(err, agent.ErrorPermissionDenied) {
			return GroupMembers{}, agent.NewError(agent.ErrorPermissionDenied, "list Discord members", errors.New("turn on the Server Members intent for the bot in the Discord Developer Portal"))
		}
		return GroupMembers{}, err
	}
	cancel()
	botPermissions, err := adapter.botPermissions(ctx, channel)
	if err != nil {
		return GroupMembers{}, err
	}
	botCanKick := botPermissions&discordgo.PermissionKickMembers != 0
	permissionChannel := adapter.permissionChannel(ctx, channel)
	users := make(map[string]string, len(members))
	result := GroupMembers{BotIsAdmin: botPermissions&discordgo.PermissionManageMessages != 0}
	for _, member := range members {
		if member == nil || member.User == nil {
			continue
		}
		permissions := channelPermissions(guild, permissionChannel, member.User.ID, member.Roles)
		if permissions&discordgo.PermissionViewChannel == 0 {
			continue
		}
		handle, err := identity.NewParticipantID()
		if err != nil {
			return GroupMembers{}, agent.NewError(agent.ErrorInternal, "create Discord member handle", errors.New("could not allocate a member handle"))
		}
		isAdmin, isSuperAdmin := roleFlags(permissions, guild.OwnerID == member.User.ID)
		isBot := member.User.ID == adapter.currentBotID()
		name := memberName(member.User, member)
		if member.User.Bot {
			name += " (bot)"
		}
		result.Members = append(result.Members, GroupMember{
			ID: handle.String(), Name: name, IsAdmin: isAdmin, IsSuperAdmin: isSuperAdmin,
			CanKick: botCanKick && !isBot && !isAdmin,
		})
		users[handle.String()] = member.User.ID
	}
	sort.SliceStable(result.Members, func(left, right int) bool {
		a, b := result.Members[left], result.Members[right]
		if a.IsAdmin != b.IsAdmin {
			return a.IsAdmin
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	adapter.memberHandlesMu.Lock()
	now := time.Now()
	for chatID, handles := range adapter.memberHandles {
		if !handles.expiresAt.After(now) {
			delete(adapter.memberHandles, chatID)
		}
	}
	adapter.memberHandles[key.ChatID.String()] = memberHandleSet{expiresAt: now.Add(memberHandleTTL), users: users}
	adapter.memberHandlesMu.Unlock()
	return result, nil
}

// KickGroupMember kicks a member the app listed with ListGroupMembers.
func (adapter *Adapter) KickGroupMember(ctx context.Context, key agent.Key, memberID string) error {
	if err := key.Validate(); err != nil {
		return err
	}
	handle, err := identity.ParseParticipantID(memberID)
	if err != nil {
		return agent.NewError(agent.ErrorInvalidArgument, "kick Discord member", errors.New("member handle is invalid"))
	}
	adapter.memberHandlesMu.Lock()
	handles, ok := adapter.memberHandles[key.ChatID.String()]
	userID := handles.users[handle.String()]
	valid := ok && handles.expiresAt.After(time.Now()) && userID != ""
	adapter.memberHandlesMu.Unlock()
	if !valid {
		return agent.NewError(agent.ErrorNotFound, "kick Discord member", errors.New("refresh the member list before trying again"))
	}
	channel, err := adapter.moderatedChannel(ctx, key)
	if err != nil {
		return err
	}
	guild, err := adapter.guild(ctx, channel.GuildID)
	if err != nil {
		return err
	}
	if member, err := adapter.member(ctx, guild.ID, userID); err == nil {
		isAdmin, _ := roleFlags(channelPermissions(guild, adapter.permissionChannel(ctx, channel), userID, member.Roles), guild.OwnerID == userID)
		if isAdmin {
			return agent.NewError(agent.ErrorPermissionDenied, "kick Discord member", errors.New("moderators can not be kicked from the app"))
		}
	}
	stripe := adapter.sendStripe(key.ChatID.String())
	stripe.Lock()
	defer stripe.Unlock()
	if err := adapter.kick(ctx, channel, userID); err != nil {
		return err
	}
	adapter.memberHandlesMu.Lock()
	if current, exists := adapter.memberHandles[key.ChatID.String()]; exists {
		delete(current.users, handle.String())
	}
	adapter.memberHandlesMu.Unlock()
	return nil
}

func (adapter *Adapter) clearMemberHandles() {
	adapter.memberHandlesMu.Lock()
	adapter.memberHandles = make(map[string]memberHandleSet)
	adapter.memberHandlesMu.Unlock()
}
