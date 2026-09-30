package discord

import (
	"context"
	"errors"
	"strings"

	"github.com/bwmarrin/discordgo"

	"github.com/Chomosuke9/DiscordAgent-Go/internal/action"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/agent"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/identity"
)

// SendText sends a reply. Discord caps a message at 2000 characters, so a
// longer reply goes out as several messages: the first quotes the target and
// is the reply's receipt, and the others are recorded as its aliases. Quiz
// choices ride on the last part as buttons.
func (adapter *Adapter) SendText(ctx context.Context, request action.SendTextRequest) (action.SendTextResult, error) {
	if !adapter.Ready() {
		return action.SendTextResult{}, agent.NewError(agent.ErrorNotReady, "send Discord message", errors.New("bot is not connected"))
	}
	channel, err := adapter.chatChannel(ctx, request.Key)
	if err != nil {
		return action.SendTextResult{}, err
	}
	reference, err := adapter.replyReference(ctx, request.Key, channel, request.QuotedMessageID)
	if err != nil {
		return action.SendTextResult{}, err
	}
	rendered := adapter.renderMentions(ctx, request.Key, channel, request.Text)
	parts := splitMessage(rendered.text, maxMessageUnits)
	if len(parts) == 0 {
		return action.SendTextResult{}, agent.NewError(agent.ErrorInvalidArgument, "send Discord message", errors.New("message text is empty"))
	}
	messages := make([]*discordgo.MessageSend, len(parts))
	for index, part := range parts {
		messages[index] = &discordgo.MessageSend{Content: part, AllowedMentions: rendered.allowed(index == 0)}
	}
	messages[0].Reference = reference
	if len(request.Choices) > 0 {
		messages[len(messages)-1].Components = quizComponents(request.Choices)
	}
	return adapter.sendAll(ctx, request.Key, channel.ID, "send Discord message", messages)
}

// SendButtons sends text with a command's buttons and menus. A layout
// Discord cannot show fails as unsupported before anything is sent, so the
// command falls back to text.
func (adapter *Adapter) SendButtons(ctx context.Context, request action.SendButtonsRequest) (action.SendTextResult, error) {
	components, err := buttonComponents(request)
	if err != nil {
		return action.SendTextResult{}, err
	}
	if !adapter.Ready() {
		return action.SendTextResult{}, agent.NewError(agent.ErrorNotReady, "send Discord buttons", errors.New("bot is not connected"))
	}
	channel, err := adapter.chatChannel(ctx, request.Key)
	if err != nil {
		return action.SendTextResult{}, err
	}
	text := strings.TrimSpace(request.Text)
	if footer := strings.TrimSpace(request.Footer); footer != "" {
		// "-#" is Discord's small subtext line.
		text += "\n-# " + strings.ReplaceAll(footer, "\n", " ")
	}
	parts := splitMessage(text, maxMessageUnits)
	messages := make([]*discordgo.MessageSend, len(parts))
	for index, part := range parts {
		messages[index] = &discordgo.MessageSend{Content: part, AllowedMentions: &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{}}}
	}
	messages[len(messages)-1].Components = components
	return adapter.sendAll(ctx, request.Key, channel.ID, "send Discord buttons", messages)
}

// sendAll sends messages in order to channelID, one chat at a time. A part
// that fails after an earlier one landed leaves the outcome unknown: the
// reply is never sent a second time.
func (adapter *Adapter) sendAll(ctx context.Context, key agent.Key, channelID, operation string, messages []*discordgo.MessageSend) (action.SendTextResult, error) {
	stripe := adapter.sendStripe(key.ChatID.String())
	stripe.Lock()
	defer stripe.Unlock()
	adapter.stopTyping(key)
	var receipts []string
	for _, message := range messages {
		sendCtx, cancel := context.WithTimeout(ctx, adapter.sendTimeout)
		sent, err := adapter.client.ChannelMessageSendComplex(channelID, message, discordgo.WithContext(sendCtx))
		if err != nil {
			err = providerError(sendCtx, operation, err)
			cancel()
			return action.SendTextResult{}, err
		}
		cancel()
		if sent == nil || sent.ID == "" {
			return action.SendTextResult{}, agent.NewError(agent.ErrorUnknownOutcome, operation, errors.New("Discord returned no message ID"))
		}
		receipts = append(receipts, sent.ID)
	}
	if len(receipts) > 1 && adapter.sent != nil {
		if err := adapter.sent.RecordReceiptAliases(ctx, key, receipts[0], receipts[1:]); err != nil {
			adapter.logger.Warn("Discord message parts could not be linked", "code", agent.CodeOf(err))
		}
	}
	return action.SendTextResult{ProviderReceipt: receipts[0]}, nil
}

// replyReference is the reference that makes a message reply to quoted, or
// nil when quoted is zero. A target from another channel fails the send
// instead of silently turning an explicit reply into an ordinary message.
func (adapter *Adapter) replyReference(ctx context.Context, key agent.Key, channel *discordgo.Channel, quoted identity.MessageID) (*discordgo.MessageReference, error) {
	if quoted.IsZero() {
		return nil, nil
	}
	chatAddress, providerMessageID, _, _, err := adapter.targets.ResolveMessageTarget(ctx, key, quoted)
	if err != nil {
		return nil, err
	}
	if chatAddress != channel.ID || providerMessageID == "" || strings.HasPrefix(providerMessageID, "tap:") {
		return nil, agent.NewError(agent.ErrorIntegrityFailure, "resolve Discord reply target", errors.New("quoted message does not belong to this channel"))
	}
	failIfMissing := false
	return &discordgo.MessageReference{MessageID: providerMessageID, ChannelID: channel.ID, GuildID: channel.GuildID, FailIfNotExists: &failIfMissing}, nil
}

// renderMentions turns the model's mention markup into Discord mentions.
func (adapter *Adapter) renderMentions(ctx context.Context, key agent.Key, channel *discordgo.Channel, text string) renderedMentions {
	resolve := func(ref identity.SenderRef) (string, bool) {
		userID, err := adapter.targets.ResolveUserID(ctx, key, ref)
		if err != nil {
			return "", false
		}
		return userID.String(), true
	}
	adminRoles := func() []string {
		guild, err := adapter.guild(ctx, channel.GuildID)
		if err != nil {
			return nil
		}
		return adminRoleIDs(guild)
	}
	return renderOutboundMentions(text, !isDirect(channel), adapter.currentBotID(), resolve, adminRoles)
}

// allowed is what the message may ping: only the mentions the model wrote,
// never an @everyone or role a user smuggled into the text. Only the first
// part pings the quoted author.
func (rendered renderedMentions) allowed(first bool) *discordgo.MessageAllowedMentions {
	allowed := &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{}, Users: rendered.users, Roles: rendered.roles, RepliedUser: first}
	if rendered.everyone {
		allowed.Parse = append(allowed.Parse, discordgo.AllowedMentionTypeEveryone)
	}
	return allowed
}
