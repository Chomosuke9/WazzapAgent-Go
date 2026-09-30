package discord

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/Chomosuke9/DiscordAgent-Go/internal/agent"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/conversation"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/identity"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/mention"
)

// candidateFromMessage turns a gateway message into the provider-neutral
// candidate the inbound pipeline consumes. The bot's own messages, other
// bots, webhooks and system messages are not conversation input.
func (adapter *Adapter) candidateFromMessage(ctx context.Context, message *discordgo.Message) (conversation.IncomingCandidate, bool) {
	if message.Author == nil || message.Author.Bot || message.Author.System || message.WebhookID != "" {
		return conversation.IncomingCandidate{}, false
	}
	if message.Type != discordgo.MessageTypeDefault && message.Type != discordgo.MessageTypeReply {
		return conversation.IncomingCandidate{}, false
	}
	senderID, err := identity.ParseUserID(message.Author.ID)
	if err != nil || message.ID == "" || message.ChannelID == "" || message.Timestamp.IsZero() {
		return conversation.IncomingCandidate{}, false
	}
	channel, err := adapter.channel(ctx, message.ChannelID)
	if err != nil {
		adapter.logger.Warn("Discord message ignored: its channel could not be read", "code", agent.CodeOf(err))
		return conversation.IncomingCandidate{}, false
	}
	botID := adapter.currentBotID()
	var guild *discordgo.Guild
	if !isDirect(channel) {
		guild, _ = adapter.client.State.Guild(channel.GuildID)
	}
	rendered := renderInboundText(message.Content, botID, adapter.markupResolver(guild))
	text := messageText(message, rendered)
	if text == "" || len(text) > conversation.MaxTextBytes {
		return conversation.IncomingCandidate{}, false
	}

	candidate := conversation.IncomingCandidate{
		TenantID: adapter.tenantID, AccountID: adapter.accountID,
		ProviderMessageID: message.ID, ProviderChatAddress: channel.ID,
		SenderUserID: senderID, SenderName: boundedName(memberName(message.Author, message.Member)),
		Text: text, OccurredAt: message.Timestamp.UTC(), ReceivedAt: time.Now().UTC(),
		Owner: adapter.gate.IsOwner(message.Author.ID),
	}
	if isDirect(channel) {
		candidate.ChatKind = conversation.ChatDirect
		candidate.ProviderAliasAddress = message.Author.ID
		candidate.Allowlisted = adapter.gate.ChatAllowlisted(conversation.ChatDirect, channel.ID, message.Author.ID)
	} else {
		candidate.ChatKind = conversation.ChatGroup
		candidate.ProviderGuildAddress = channel.GuildID
		if channel.IsThread() {
			candidate.ProviderAliasAddress = channel.ParentID
		}
		candidate.Allowlisted = adapter.gate.ChatAllowlisted(conversation.ChatGroup, channel.ID, channel.GuildID, channel.ParentID)
		var roles []string
		if message.Member != nil {
			roles = message.Member.Roles
			if roles == nil {
				roles = []string{}
			}
		}
		if permissions, err := adapter.memberPermissions(ctx, channel, message.Author.ID, roles); err == nil {
			owner := guild != nil && guild.OwnerID == message.Author.ID
			candidate.SenderIsAdmin, candidate.SenderIsSuperAdmin = roleFlags(permissions, owner)
		}
		adapter.rememberChannelName(ctx, channel)
	}

	if reference := message.MessageReference; reference != nil && reference.Type == discordgo.MessageReferenceTypeDefault &&
		reference.MessageID != "" && (reference.ChannelID == "" || reference.ChannelID == channel.ID) {
		candidate.ProviderQuotedMessageID = reference.MessageID
		if strings.EqualFold(strings.TrimSpace(text), "/catch") && message.ReferencedMessage != nil {
			if encoded, err := json.Marshal(message.ReferencedMessage); err == nil && len(encoded) <= conversation.MaxRawQuotedMessageBytes {
				candidate.ProviderQuotedMessageJSON = encoded
				fromMe := message.ReferencedMessage.Author != nil && message.ReferencedMessage.Author.ID == botID
				candidate.ProviderQuotedFromMe = &fromMe
			}
		}
	}
	candidate.Mentions, candidate.MentionsBot = adapter.inboundMentions(text, message, guild, botID)
	return candidate, true
}

// inboundMentions binds each "@<user ID>" token left in text to its user.
func (adapter *Adapter) inboundMentions(text string, message *discordgo.Message, guild *discordgo.Guild, botID string) ([]conversation.IncomingMention, bool) {
	var mentions []conversation.IncomingMention
	mentionsBot := false
	seen := make(map[string]struct{}, len(message.Mentions)+1)
	add := func(userID string, bind func() conversation.IncomingMention) {
		token := "@" + userID
		if _, exists := seen[token]; exists || len(mentions) == conversation.MaxMentions || !mention.Contains(text, token) {
			return
		}
		seen[token] = struct{}{}
		mentions = append(mentions, bind())
	}
	if botID != "" {
		add(botID, func() conversation.IncomingMention {
			mentionsBot = true
			return conversation.IncomingMention{Token: "@" + botID, Bot: true}
		})
	}
	for _, user := range message.Mentions {
		if user == nil || user.ID == botID {
			continue
		}
		target, err := identity.ParseUserID(user.ID)
		if err != nil {
			continue
		}
		add(user.ID, func() conversation.IncomingMention {
			var member *discordgo.Member
			if guild != nil {
				member, _ = adapter.client.State.Member(guild.ID, user.ID)
			}
			return conversation.IncomingMention{Token: "@" + user.ID, TargetUserID: target, DisplayName: boundedName(memberName(user, member))}
		})
	}
	return mentions, mentionsBot
}

// candidateFromInteraction turns a tap on one of the bot's buttons or menus
// into the user's message. It replies to the message that carried the
// component, so it counts as a reply to the bot.
func (adapter *Adapter) candidateFromInteraction(ctx context.Context, interaction *discordgo.Interaction) (conversation.IncomingCandidate, bool) {
	user, member := interaction.User, interaction.Member
	if member != nil && member.User != nil {
		user = member.User
	}
	if user == nil || user.Bot || interaction.Message == nil || interaction.ChannelID == "" {
		return conversation.IncomingCandidate{}, false
	}
	userID, err := identity.ParseUserID(user.ID)
	if err != nil {
		return conversation.IncomingCandidate{}, false
	}
	text := strings.TrimSpace(tappedText(interaction.MessageComponentData(), interaction.Message))
	if text == "" || len(text) > conversation.MaxTextBytes {
		return conversation.IncomingCandidate{}, false
	}
	channel, err := adapter.channel(ctx, interaction.ChannelID)
	if err != nil {
		adapter.logger.Warn("Discord tap ignored: its channel could not be read", "code", agent.CodeOf(err))
		return conversation.IncomingCandidate{}, false
	}
	candidate := conversation.IncomingCandidate{
		TenantID: adapter.tenantID, AccountID: adapter.accountID,
		ProviderMessageID: "tap:" + interaction.ID, ProviderQuotedMessageID: interaction.Message.ID,
		ProviderChatAddress: channel.ID, SenderUserID: userID, SenderName: boundedName(memberName(user, member)),
		Text: text, OccurredAt: time.Now().UTC(), ReceivedAt: time.Now().UTC(),
		Owner: adapter.gate.IsOwner(user.ID),
	}
	if created, err := discordgo.SnowflakeTimestamp(interaction.ID); err == nil {
		candidate.OccurredAt = created.UTC()
	}
	if isDirect(channel) {
		candidate.ChatKind = conversation.ChatDirect
		candidate.ProviderAliasAddress = user.ID
		candidate.Allowlisted = adapter.gate.ChatAllowlisted(conversation.ChatDirect, channel.ID, user.ID)
		return candidate, true
	}
	candidate.ChatKind = conversation.ChatGroup
	candidate.ProviderGuildAddress = channel.GuildID
	if channel.IsThread() {
		candidate.ProviderAliasAddress = channel.ParentID
	}
	candidate.Allowlisted = adapter.gate.ChatAllowlisted(conversation.ChatGroup, channel.ID, channel.GuildID, channel.ParentID)
	if member != nil {
		// Discord computes the tapper's permissions in this channel for us.
		guild, _ := adapter.client.State.Guild(channel.GuildID)
		candidate.SenderIsAdmin, candidate.SenderIsSuperAdmin = roleFlags(member.Permissions, guild != nil && guild.OwnerID == user.ID)
	}
	return candidate, true
}

func (adapter *Adapter) markupResolver(guild *discordgo.Guild) markupResolver {
	botID := adapter.currentBotID()
	return markupResolver{
		role: func(id string) (string, bool) {
			if guild == nil {
				return "", false
			}
			for _, role := range guild.Roles {
				if role == nil || role.ID != id {
					continue
				}
				// A bot's integration role is managed, and the bot holds it.
				botRole := false
				if role.Managed {
					if member, err := adapter.client.State.Member(guild.ID, botID); err == nil {
						for _, held := range member.Roles {
							botRole = botRole || held == id
						}
					}
				}
				return role.Name, botRole
			}
			return "", false
		},
		channel: func(id string) string {
			if channel, err := adapter.client.State.Channel(id); err == nil && channel != nil {
				return channel.Name
			}
			return ""
		},
	}
}

func boundedName(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= agent.MaxDisplayNameBytes {
		return value
	}
	cut := value[:agent.MaxDisplayNameBytes]
	for len(cut) > 0 && !validUTF8Suffix(cut) {
		cut = cut[:len(cut)-1]
	}
	return strings.TrimSpace(cut)
}

func validUTF8Suffix(value string) bool {
	return strings.ToValidUTF8(value, "�") == value
}
