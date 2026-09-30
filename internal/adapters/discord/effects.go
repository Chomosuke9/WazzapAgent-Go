package discord

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/Chomosuke9/DiscordAgent-Go/internal/agent"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/effect"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/identity"
)

// typingRefresh is how often the typing indicator is renewed; Discord shows
// it for about ten seconds.
const typingRefresh = 8 * time.Second

// MarkRead does nothing: bots have no read receipts on Discord.
func (adapter *Adapter) MarkRead(context.Context, agent.Key, identity.MessageID) error { return nil }

// SetComposing shows the typing indicator while the model works, renewing it
// until composing is turned off or a message is sent.
func (adapter *Adapter) SetComposing(ctx context.Context, key agent.Key, composing bool) error {
	if !composing {
		adapter.stopTyping(key)
		return nil
	}
	if !adapter.Ready() {
		return agent.NewError(agent.ErrorNotReady, "show Discord typing", errors.New("bot is not connected"))
	}
	channel, err := adapter.chatChannel(ctx, key)
	if err != nil {
		return err
	}
	typingCtx, cancel := context.WithCancel(adapter.rootCtx)
	adapter.typingMu.Lock()
	if previous, exists := adapter.typing[key.ChatID.String()]; exists {
		previous()
	}
	adapter.typing[key.ChatID.String()] = cancel
	adapter.typingMu.Unlock()
	go func() {
		ticker := time.NewTicker(typingRefresh)
		defer ticker.Stop()
		for {
			requestCtx, cancelRequest := context.WithTimeout(typingCtx, adapter.sendTimeout)
			_ = adapter.client.ChannelTyping(channel.ID, discordgo.WithContext(requestCtx))
			cancelRequest()
			select {
			case <-typingCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return nil
}

func (adapter *Adapter) stopTyping(key agent.Key) {
	adapter.typingMu.Lock()
	defer adapter.typingMu.Unlock()
	if cancel, exists := adapter.typing[key.ChatID.String()]; exists {
		cancel()
		delete(adapter.typing, key.ChatID.String())
	}
}

func (adapter *Adapter) stopAllTyping() {
	adapter.typingMu.Lock()
	defer adapter.typingMu.Unlock()
	for chatID, cancel := range adapter.typing {
		cancel()
		delete(adapter.typing, chatID)
	}
}

// DeleteMessage removes a stored message, such as one from a muted member.
func (adapter *Adapter) DeleteMessage(ctx context.Context, key agent.Key, messageID identity.MessageID) error {
	if !adapter.Ready() {
		return agent.NewError(agent.ErrorNotReady, "delete Discord message", errors.New("bot is not connected"))
	}
	channel, providerMessageID, sender, err := adapter.resolveEffectTarget(ctx, key, messageID)
	if err != nil {
		return err
	}
	stripe := adapter.sendStripe(key.ChatID.String())
	stripe.Lock()
	defer stripe.Unlock()
	return adapter.deleteMessage(ctx, channel, providerMessageID, sender)
}

func (adapter *Adapter) deleteMessage(ctx context.Context, channel *discordgo.Channel, providerMessageID, sender string) error {
	if sender != "" && sender != adapter.currentBotID() {
		if isDirect(channel) {
			return agent.NewError(agent.ErrorPermissionDenied, "delete Discord message", errors.New("a bot can not delete other people's messages in a direct chat"))
		}
		permissions, err := adapter.botPermissions(ctx, channel)
		if err != nil {
			return err
		}
		if permissions&discordgo.PermissionManageMessages == 0 {
			return agent.NewError(agent.ErrorPermissionDenied, "delete Discord message", errors.New("the bot needs the Manage Messages permission in this channel"))
		}
	}
	requestCtx, cancel := context.WithTimeout(ctx, adapter.sendTimeout)
	defer cancel()
	if err := adapter.client.ChannelMessageDelete(channel.ID, providerMessageID, discordgo.WithContext(requestCtx)); err != nil {
		return providerError(requestCtx, "delete Discord message", err)
	}
	return nil
}

// ExecuteEffect is the native edge for a typed effect. The effect carries
// only internal IDs; Discord IDs are resolved here, after the dispatcher's
// policy recheck.
func (adapter *Adapter) ExecuteEffect(ctx context.Context, stored effect.Stored) (string, error) {
	if !adapter.Ready() {
		return "", agent.NewError(agent.ErrorNotReady, "execute Discord effect", errors.New("bot is not connected"))
	}
	key := stored.Request.Ref.Key
	switch value := stored.Request.Effect.(type) {
	case effect.Sticker:
		return adapter.sendSticker(ctx, key, value)
	case effect.React:
		channel, providerMessageID, _, err := adapter.resolveEffectTarget(ctx, key, value.TargetMessageID)
		if err != nil {
			return "", err
		}
		requestCtx, cancel := context.WithTimeout(ctx, adapter.sendTimeout)
		defer cancel()
		if err := adapter.client.MessageReactionAdd(channel.ID, providerMessageID, strings.TrimSpace(value.Emoji), discordgo.WithContext(requestCtx)); err != nil {
			return "", providerError(requestCtx, "add Discord reaction", err)
		}
		return providerMessageID, nil
	case effect.DeleteMessage:
		channel, providerMessageID, sender, err := adapter.resolveEffectTarget(ctx, key, value.TargetMessageID)
		if err != nil {
			return "", err
		}
		stripe := adapter.sendStripe(key.ChatID.String())
		stripe.Lock()
		defer stripe.Unlock()
		if err := adapter.deleteMessage(ctx, channel, providerMessageID, sender); err != nil {
			return "", err
		}
		return providerMessageID, nil
	default:
		return "", agent.NewError(agent.ErrorIntegrityFailure, "execute Discord effect", errors.New("effect type is invalid"))
	}
}

// resolveEffectTarget maps a stored message to its channel, Discord message
// ID and author. A tap on a button is not a message and cannot be targeted.
func (adapter *Adapter) resolveEffectTarget(ctx context.Context, key agent.Key, target identity.MessageID) (*discordgo.Channel, string, string, error) {
	address, providerMessageID, sender, _, err := adapter.targets.ResolveMessageTarget(ctx, key, target)
	if err != nil {
		return nil, "", "", err
	}
	if providerMessageID == "" || strings.HasPrefix(providerMessageID, "tap:") {
		return nil, "", "", agent.NewError(agent.ErrorInvalidArgument, "resolve Discord message", errors.New("the target is a button tap, not a message"))
	}
	if _, err := identity.ParseUserID(address); err != nil {
		return nil, "", "", agent.NewError(agent.ErrorIntegrityFailure, "resolve Discord message", errors.New("stored channel ID is invalid"))
	}
	channel, err := adapter.channel(ctx, address)
	if err != nil {
		return nil, "", "", err
	}
	return channel, providerMessageID, sender, nil
}

// StickerNames lists the stickers the model may send in this chat: the
// server's own stickers, by a name reduced to lowercase letters, digits, "-"
// and "_". A direct chat has none.
func (adapter *Adapter) StickerNames(ctx context.Context, key agent.Key) ([]string, error) {
	if !adapter.Ready() {
		return nil, nil
	}
	channel, err := adapter.chatChannel(ctx, key)
	if err != nil || isDirect(channel) {
		return nil, err
	}
	catalog := stickerCatalog(adapter.guildStickers(channel.GuildID))
	names := make([]string, 0, len(catalog))
	for name := range catalog {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// stickerCatalog maps each usable sticker's model-facing name to its ID. Two
// stickers that reduce to one name are told apart by a number.
func stickerCatalog(stickers []*discordgo.Sticker) map[string]string {
	catalog := make(map[string]string, len(stickers))
	for _, sticker := range stickers {
		if sticker == nil || !sticker.Available || sticker.ID == "" {
			continue
		}
		base := stickerName(sticker.Name)
		if base == "" {
			continue
		}
		name := base
		for index := 2; ; index++ {
			if _, taken := catalog[name]; !taken {
				break
			}
			suffix := "-" + strconv.Itoa(index)
			if len(base)+len(suffix) > 64 {
				base = base[:64-len(suffix)]
			}
			name = base + suffix
		}
		catalog[name] = sticker.ID
	}
	return catalog
}

func stickerName(raw string) string {
	var builder strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(raw)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			builder.WriteRune(r)
			lastDash = false
		case r == '-' || r == ' ':
			if !lastDash && builder.Len() > 0 {
				builder.WriteByte('-')
				lastDash = true
			}
		}
		if builder.Len() >= 64 {
			break
		}
	}
	return strings.TrimRight(builder.String(), "-")
}

// sendSticker sends the server sticker the model chose. A sticker removed
// since the model saw the list fails without sending anything.
func (adapter *Adapter) sendSticker(ctx context.Context, key agent.Key, value effect.Sticker) (string, error) {
	channel, err := adapter.chatChannel(ctx, key)
	if err != nil {
		return "", err
	}
	if isDirect(channel) {
		return "", agent.NewError(agent.ErrorNotFound, "send Discord sticker", errors.New("direct chats have no server stickers"))
	}
	stickerID, exists := stickerCatalog(adapter.guildStickers(channel.GuildID))[value.Name]
	if !exists {
		return "", agent.NewError(agent.ErrorNotFound, "send Discord sticker", errors.New("the server has no sticker by that name"))
	}
	reference, err := adapter.replyReference(ctx, key, channel, value.QuotedMessageID)
	if err != nil {
		return "", err
	}
	message := &discordgo.MessageSend{StickerIDs: []string{stickerID}, Reference: reference,
		AllowedMentions: &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{}}}
	result, err := adapter.sendAll(ctx, key, channel.ID, "send Discord sticker", []*discordgo.MessageSend{message})
	if err != nil {
		return "", err
	}
	if adapter.sent != nil {
		// The sticker is already sent: failing now would send it again, so a
		// failed record only means replies to it do not count as replies to
		// the bot.
		if recordErr := adapter.sent.RecordSentSticker(ctx, key, result.ProviderReceipt, value.Name); recordErr != nil {
			adapter.logger.Warn("record sent sticker failed", "code", agent.CodeOf(recordErr))
		}
	}
	return result.ProviderReceipt, nil
}

// guildStickers copies a server's stickers from the state, which gateway
// events update concurrently.
func (adapter *Adapter) guildStickers(guildID string) []*discordgo.Sticker {
	guild, err := adapter.client.State.Guild(guildID)
	if err != nil {
		return nil
	}
	adapter.client.State.RLock()
	defer adapter.client.State.RUnlock()
	return append([]*discordgo.Sticker(nil), guild.Stickers...)
}

func (adapter *Adapter) onGuildStickersUpdate(_ *discordgo.Session, event *discordgo.GuildStickersUpdate) {
	if event == nil {
		return
	}
	guild, err := adapter.client.State.Guild(event.GuildID)
	if err != nil {
		return
	}
	adapter.client.State.Lock()
	guild.Stickers = event.Stickers
	adapter.client.State.Unlock()
}
