package discord

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/Chomosuke9/DiscordAgent-Go/internal/agent"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/conversation"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/identity"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/policy"
)

// channel returns a channel from the gateway state, or from the REST API
// when the state has not seen it yet, remembering what it fetched.
func (adapter *Adapter) channel(ctx context.Context, id string) (*discordgo.Channel, error) {
	if channel, err := adapter.client.State.Channel(id); err == nil && channel != nil {
		return channel, nil
	}
	requestCtx, cancel := context.WithTimeout(ctx, adapter.sendTimeout)
	defer cancel()
	channel, err := adapter.client.Channel(id, discordgo.WithContext(requestCtx))
	if err != nil {
		return nil, providerError(requestCtx, "read Discord channel", err)
	}
	_ = adapter.client.State.ChannelAdd(channel)
	return channel, nil
}

func (adapter *Adapter) guild(ctx context.Context, id string) (*discordgo.Guild, error) {
	if guild, err := adapter.client.State.Guild(id); err == nil && guild != nil {
		return guild, nil
	}
	requestCtx, cancel := context.WithTimeout(ctx, adapter.sendTimeout)
	defer cancel()
	guild, err := adapter.client.Guild(id, discordgo.WithContext(requestCtx))
	if err != nil {
		return nil, providerError(requestCtx, "read Discord server", err)
	}
	return guild, nil
}

// member returns a server member, fetching and remembering them when the
// state does not know them: without the privileged members intent, Discord
// sends only the members it has to.
func (adapter *Adapter) member(ctx context.Context, guildID, userID string) (*discordgo.Member, error) {
	if member, err := adapter.client.State.Member(guildID, userID); err == nil && member != nil {
		return member, nil
	}
	requestCtx, cancel := context.WithTimeout(ctx, adapter.sendTimeout)
	defer cancel()
	member, err := adapter.client.GuildMember(guildID, userID, discordgo.WithContext(requestCtx))
	if err != nil {
		return nil, providerError(requestCtx, "read Discord member", err)
	}
	member.GuildID = guildID
	_ = adapter.client.State.MemberAdd(member)
	return member, nil
}

// permissionChannel is the channel whose overwrites apply: a thread uses
// its parent's.
func (adapter *Adapter) permissionChannel(ctx context.Context, channel *discordgo.Channel) *discordgo.Channel {
	if channel != nil && channel.IsThread() && channel.ParentID != "" {
		if parent, err := adapter.channel(ctx, channel.ParentID); err == nil {
			return parent
		}
	}
	return channel
}

// memberPermissions computes userID's permissions in channel. roles, when
// not nil, are the member's roles as the event carried them.
func (adapter *Adapter) memberPermissions(ctx context.Context, channel *discordgo.Channel, userID string, roles []string) (int64, error) {
	if channel == nil || channel.GuildID == "" {
		return 0, nil
	}
	guild, err := adapter.guild(ctx, channel.GuildID)
	if err != nil {
		return 0, err
	}
	if roles == nil && userID != guild.OwnerID {
		member, err := adapter.member(ctx, guild.ID, userID)
		if err != nil {
			return 0, err
		}
		roles = member.Roles
	}
	return channelPermissions(guild, adapter.permissionChannel(ctx, channel), userID, roles), nil
}

func (adapter *Adapter) botPermissions(ctx context.Context, channel *discordgo.Channel) (int64, error) {
	return adapter.memberPermissions(ctx, channel, adapter.currentBotID(), nil)
}

// isDirect reports whether channel is a direct-message channel.
func isDirect(channel *discordgo.Channel) bool {
	return channel.Type == discordgo.ChannelTypeDM || channel.Type == discordgo.ChannelTypeGroupDM
}

// chatChannel resolves the chat key to its Discord channel.
func (adapter *Adapter) chatChannel(ctx context.Context, key agent.Key) (*discordgo.Channel, error) {
	address, err := adapter.targets.ResolveChatAddress(ctx, key)
	if err != nil {
		return nil, err
	}
	if _, err := identity.ParseUserID(address); err != nil {
		return nil, agent.NewError(agent.ErrorIntegrityFailure, "resolve Discord channel", errors.New("stored channel ID is invalid"))
	}
	return adapter.channel(ctx, address)
}

// channelTitle names a server channel the way people refer to it:
// "#general · My Server".
func (adapter *Adapter) channelTitle(ctx context.Context, channel *discordgo.Channel) string {
	name := "#" + strings.TrimSpace(channel.Name)
	if channel.GuildID == "" {
		return name
	}
	if guild, err := adapter.client.State.Guild(channel.GuildID); err == nil && strings.TrimSpace(guild.Name) != "" {
		return name + " · " + strings.TrimSpace(guild.Name)
	}
	if guild, err := adapter.guild(ctx, channel.GuildID); err == nil && strings.TrimSpace(guild.Name) != "" {
		return name + " · " + strings.TrimSpace(guild.Name)
	}
	return name
}

// ReadChatContext returns the provider-neutral details for the model's
// chat-information block. It does not grant authority for native effects.
func (adapter *Adapter) ReadChatContext(ctx context.Context, key agent.Key) (agent.ChatContext, error) {
	if err := key.Validate(); err != nil {
		return agent.ChatContext{}, err
	}
	if !adapter.Ready() {
		return agent.ChatContext{}, agent.NewError(agent.ErrorNotReady, "read Discord chat context", errors.New("bot is not connected"))
	}
	channel, err := adapter.chatChannel(ctx, key)
	if err != nil {
		return agent.ChatContext{}, err
	}
	if isDirect(channel) {
		return agent.ChatContext{Kind: "private"}, nil
	}
	result := agent.ChatContext{Kind: "group", Name: adapter.channelTitle(ctx, channel), Description: channel.Topic}
	if permissions, err := adapter.botPermissions(ctx, channel); err == nil {
		result.BotIsAdmin = permissions&discordgo.PermissionManageMessages != 0
	}
	if err := result.Validate(); err != nil {
		return agent.ChatContext{}, agent.NewError(agent.ErrorIntegrityFailure, "read Discord chat context", err)
	}
	return result, nil
}

// ReadChatAuthority reads current permissions for policy. The bot counts as
// a group admin when it may manage messages in the channel; each moderation
// action still checks the permission it needs.
func (adapter *Adapter) ReadChatAuthority(ctx context.Context, principal policy.Principal) (policy.ChatAuthority, error) {
	if err := principal.Validate(); err != nil {
		return policy.ChatAuthority{}, err
	}
	if !adapter.Ready() {
		return policy.ChatAuthority{}, agent.NewError(agent.ErrorNotReady, "read Discord chat authority", errors.New("bot is not connected"))
	}
	channel, err := adapter.chatChannel(ctx, principal.Key())
	if err != nil {
		return policy.ChatAuthority{}, err
	}
	observedAt := time.Now().UTC().UnixMilli()
	if isDirect(channel) {
		return policy.ChatAuthority{ChatKind: conversation.ChatDirect, ObservedAt: observedAt}, nil
	}
	authority := policy.ChatAuthority{ChatKind: conversation.ChatGroup, ObservedAt: observedAt}
	botPermissions, err := adapter.botPermissions(ctx, channel)
	if err != nil {
		return policy.ChatAuthority{}, err
	}
	authority.BotIsAdmin = botPermissions&discordgo.PermissionManageMessages != 0
	if principal.Kind == policy.PrincipalHuman {
		permissions, err := adapter.memberPermissions(ctx, channel, principal.UserID.String(), nil)
		if err != nil {
			if !agent.IsCode(err, agent.ErrorNotFound) {
				return policy.ChatAuthority{}, err
			}
			permissions = 0 // they left the server
		}
		guild, _ := adapter.guild(ctx, channel.GuildID)
		authority.ActorIsAdmin, _ = roleFlags(permissions, guild != nil && guild.OwnerID == principal.UserID.String())
	}
	return authority, authority.Validate()
}

// memberName is how a member appears: server nickname, then display name,
// then username.
func memberName(user *discordgo.User, member *discordgo.Member) string {
	if member != nil && strings.TrimSpace(member.Nick) != "" {
		return strings.TrimSpace(member.Nick)
	}
	if user == nil {
		return ""
	}
	if name := strings.TrimSpace(user.GlobalName); name != "" {
		return name
	}
	return strings.TrimSpace(user.Username)
}

// rememberChannelName saves a server channel's name once per change, so the
// app lists chats by name.
func (adapter *Adapter) rememberChannelName(ctx context.Context, channel *discordgo.Channel) {
	if adapter.channelNames == nil || channel == nil || channel.GuildID == "" || isDirect(channel) {
		return
	}
	title := adapter.channelTitle(ctx, channel)
	adapter.namesMu.Lock()
	unchanged := adapter.savedNames[channel.ID] == title
	adapter.namesMu.Unlock()
	if unchanged {
		return
	}
	if err := adapter.channelNames.SaveGroupName(ctx, adapter.tenantID, adapter.accountID, channel.ID, title); err != nil {
		adapter.logger.Warn("Discord channel name could not be stored", "code", agent.CodeOf(err))
		return
	}
	adapter.namesMu.Lock()
	adapter.savedNames[channel.ID] = title
	adapter.namesMu.Unlock()
}

func (adapter *Adapter) onGuildCreate(_ *discordgo.Session, event *discordgo.GuildCreate) {
	if event == nil || event.Guild == nil || adapter.rootCtx == nil {
		return
	}
	for _, channel := range event.Guild.Channels {
		if channel != nil {
			channel.GuildID = event.Guild.ID
			adapter.rememberChannelName(adapter.rootCtx, channel)
		}
	}
}

func (adapter *Adapter) onChannelUpdate(_ *discordgo.Session, event *discordgo.ChannelUpdate) {
	if event == nil || event.Channel == nil || adapter.rootCtx == nil {
		return
	}
	adapter.rememberChannelName(adapter.rootCtx, event.Channel)
}
