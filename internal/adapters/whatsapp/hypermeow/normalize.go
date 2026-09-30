package hypermeow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/types"
	"github.com/polymorfa/hypermeow/types/events"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/Chomosuke9/DiscordAgent-Go/internal/agent"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/conversation"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/identity"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/mention"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/policy"
)

// messageNormalizer turns a native message event into the provider-neutral
// candidate the inbound pipeline consumes. It reads the device store for the
// account's own identity and contact names, asks the group cache for sender
// roles, and takes trust flags from the policy gate. It never sends anything.
type messageNormalizer struct {
	tenantID   identity.TenantID
	accountID  identity.AccountID
	gate       policy.InboundGate
	client     *whatsmeow.Client
	groupRoles func(ctx context.Context, chat, sender types.JID) (isAdmin, isSuperAdmin bool)
}

func ignoredNativeReason(event *events.Message) string {
	if event == nil || event.Message == nil {
		return "invalid_event"
	}
	if event.IsEdit {
		return "edited_message"
	}
	if text, _ := messageContent(event.Message); text == "" {
		return "unsupported_content"
	}
	return "invalid_metadata"
}

func (normalizer *messageNormalizer) normalizeMessage(ctx context.Context, event *events.Message) (conversation.IncomingCandidate, bool) {
	if event == nil || event.Message == nil || event.IsEdit {
		return conversation.IncomingCandidate{}, false
	}
	text, contextInfo := messageContent(event.Message)
	if text == "" {
		return conversation.IncomingCandidate{}, false
	}
	text = renderGroupMentions(text, contextInfo)
	chat := event.Info.Chat.ToNonAD()
	sender := event.Info.Sender.ToNonAD()
	senderLID, ok := lidAddress(sender, event.Info.SenderAlt)
	if !ok {
		// Identity is fail-closed: never invent a senderRef from a phone alias.
		return conversation.IncomingCandidate{}, false
	}
	senderPhone := phoneAddress(sender, event.Info.SenderAlt)
	if !event.Info.IsGroup {
		if event.Info.IsFromMe {
			chat = phoneAddress(chat, event.Info.RecipientAlt)
		} else {
			chat = phoneAddress(chat, event.Info.SenderAlt)
		}
	}
	if chat.IsEmpty() || event.Info.ID == "" || event.Info.Timestamp.IsZero() {
		return conversation.IncomingCandidate{}, false
	}
	chatKind := conversation.ChatDirect
	if chat == types.StatusBroadcastJID {
		chatKind = conversation.ChatStatus
	} else if event.Info.IsGroup {
		chatKind = conversation.ChatGroup
	}
	chatAddress := chat.String()
	allowlisted := normalizer.gate.ChatAllowlisted(chatKind, chatAddress, jidString(event.Info.SenderAlt), jidString(event.Info.RecipientAlt))
	mentioned := false
	mentions := []conversation.IncomingMention(nil)
	quotedMessageID := ""
	quotedMessageJSON := []byte(nil)
	var quotedMessageFromMe *bool
	if contextInfo != nil {
		quotedMessageID = contextInfo.GetStanzaID()
		if strings.EqualFold(strings.TrimSpace(text), "/catch") && quotedMessageID != "" && contextInfo.GetQuotedMessage() != nil {
			quotedMessageJSON, _ = protojson.Marshal(contextInfo.GetQuotedMessage())
			if len(quotedMessageJSON) == 0 {
				quotedMessageFromMe = nil
			} else {
				quotedMessageFromMe = normalizer.quotedMessageFromMe(contextInfo.GetParticipant())
			}
		}
	}
	if chatKind == conversation.ChatGroup && contextInfo != nil {
		mentioned = normalizer.mentionsOwnAccount(contextInfo.GetMentionedJID())
	}
	senderIsAdmin, senderIsSuperAdmin := normalizer.groupRoles(ctx, chat, sender)
	if contextInfo != nil {
		mentions = normalizer.extractInboundMentions(ctx, text, contextInfo.GetMentionedJID())
	}
	senderName := strings.TrimSpace(event.Info.PushName)
	if senderName == "" {
		senderName = normalizer.contactPushName(ctx, sender, event.Info.SenderAlt, senderPhone)
	}
	return conversation.IncomingCandidate{
		TenantID:                  normalizer.tenantID,
		AccountID:                 normalizer.accountID,
		ProviderMessageID:         string(event.Info.ID),
		ProviderQuotedMessageID:   quotedMessageID,
		ProviderQuotedMessageJSON: quotedMessageJSON,
		ProviderQuotedFromMe:      quotedMessageFromMe,
		ProviderMediaJSON:         commandMedia(text, event.Message, contextInfo),
		ProviderChatAddress:       chatAddress,
		SenderLID:                 mustLID(senderLID),
		ProviderSenderPhone:       jidString(senderPhone),
		SenderName:                senderName,
		SenderIsAdmin:             senderIsAdmin,
		SenderIsSuperAdmin:        senderIsSuperAdmin,
		ChatKind:                  chatKind,
		Text:                      text,
		Mentions:                  mentions,
		MentionsBot:               mentioned,
		FromMe:                    event.Info.IsFromMe,
		Owner:                     normalizer.gate.IsOwner(jidString(sender), jidString(event.Info.SenderAlt)),
		Allowlisted:               allowlisted,
		OccurredAt:                event.Info.Timestamp.UTC(),
		ReceivedAt:                time.Now().UTC(),
	}, true
}

// renderGroupMentions shows a group mention, such as the one WhatsApp sends
// for "@admin", by its subject instead of the raw group address in the text.
func renderGroupMentions(text string, contextInfo *waE2E.ContextInfo) string {
	for _, groupMention := range contextInfo.GetGroupMentions() {
		address, subject := groupMention.GetGroupJID(), strings.TrimSpace(groupMention.GetGroupSubject())
		if address == "" || subject == "" || strings.ContainsAny(subject, "\r\n") {
			continue
		}
		text = strings.ReplaceAll(text, "@"+address, "@"+subject)
	}
	return text
}

// messageContent returns the text the pipeline sees and the context info that
// carries its mentions and quote. A button tap routes by its ID before anything
// else, so a "/command args" button re-enters its command like typed text.
// Media is still text-only for the model, but the transcript must not lose it:
// it becomes a placeholder followed by its caption, and a caption that starts
// with "/" stays bare so it routes as a command.
func messageContent(message *waE2E.Message) (string, *waE2E.ContextInfo) {
	if text, contextInfo := buttonReply(message); text != "" {
		return text, contextInfo
	}
	if text := message.GetConversation(); text != "" {
		return text, message.GetExtendedTextMessage().GetContextInfo()
	}
	if extended := message.GetExtendedTextMessage(); extended.GetText() != "" {
		return extended.GetText(), extended.GetContextInfo()
	}
	media := func(placeholder, caption string, contextInfo *waE2E.ContextInfo) (string, *waE2E.ContextInfo) {
		caption = strings.TrimSpace(caption)
		switch {
		case caption == "":
			return placeholder, contextInfo
		case strings.HasPrefix(caption, "/"):
			return caption, contextInfo
		default:
			return placeholder + " " + caption, contextInfo
		}
	}
	// Protobuf getters are nil-safe, so each branch runs only when present.
	if image := message.GetImageMessage(); image != nil {
		return media(conversation.PlaceholderImage, image.GetCaption(), image.GetContextInfo())
	}
	if video := message.GetVideoMessage(); video != nil {
		if video.GetGifPlayback() {
			return media(conversation.PlaceholderGIF, video.GetCaption(), video.GetContextInfo())
		}
		return media(conversation.PlaceholderVideo, video.GetCaption(), video.GetContextInfo())
	}
	if document := message.GetDocumentMessage(); document != nil {
		return media(conversation.PlaceholderDocument, document.GetCaption(), document.GetContextInfo())
	}
	if audio := message.GetAudioMessage(); audio != nil {
		if audio.GetPTT() {
			return media(conversation.PlaceholderVoiceNote, "", audio.GetContextInfo())
		}
		return media(conversation.PlaceholderAudio, "", audio.GetContextInfo())
	}
	if sticker := message.GetStickerMessage(); sticker != nil {
		return media(conversation.PlaceholderSticker, "", sticker.GetContextInfo())
	}
	return "", nil
}

// buttonReply extracts a tap on a list, buttons, template, or native-flow
// message. Slash-command IDs become the message text; any other ID yields the
// label the user saw, so the conversation reads naturally.
func buttonReply(message *waE2E.Message) (string, *waE2E.ContextInfo) {
	pick := func(id, label string) string {
		if strings.HasPrefix(id, "/") || strings.TrimSpace(label) == "" {
			return id
		}
		return label
	}
	// Protobuf getters are nil-safe, so each chain is empty when absent.
	if list := message.GetListResponseMessage(); list.GetSingleSelectReply().GetSelectedRowID() != "" {
		return pick(list.GetSingleSelectReply().GetSelectedRowID(), list.GetTitle()), list.GetContextInfo()
	}
	if buttons := message.GetButtonsResponseMessage(); buttons.GetSelectedButtonID() != "" {
		return pick(buttons.GetSelectedButtonID(), buttons.GetSelectedDisplayText()), buttons.GetContextInfo()
	}
	if template := message.GetTemplateButtonReplyMessage(); template.GetSelectedID() != "" {
		return pick(template.GetSelectedID(), template.GetSelectedDisplayText()), template.GetContextInfo()
	}
	interactive := message.GetInteractiveResponseMessage()
	if raw := interactive.GetNativeFlowResponseMessage().GetParamsJSON(); raw != "" {
		var params map[string]any
		if err := json.Unmarshal([]byte(raw), &params); err == nil && params["id"] != nil {
			// The id is usually a string, but numeric ids are valid too.
			return pick(fmt.Sprint(params["id"]), interactive.GetBody().GetText()), interactive.GetContextInfo()
		}
	}
	return "", nil
}

func (normalizer *messageNormalizer) quotedMessageFromMe(participant string) *bool {
	if participant == "" {
		return nil
	}
	if _, err := types.ParseJID(participant); err != nil {
		return nil
	}
	fromMe := normalizer.mentionsOwnAccount([]string{participant})
	return &fromMe
}

func (normalizer *messageNormalizer) contactPushName(ctx context.Context, addresses ...types.JID) string {
	if normalizer.client == nil || normalizer.client.Store == nil || normalizer.client.Store.Contacts == nil {
		return ""
	}
	for _, address := range addresses {
		address = address.ToNonAD()
		if address.IsEmpty() {
			continue
		}
		contact, err := normalizer.client.Store.Contacts.GetContact(ctx, address)
		if err != nil {
			continue
		}
		if pushName := strings.TrimSpace(contact.PushName); pushName != "" {
			return pushName
		}
	}
	return ""
}

func (normalizer *messageNormalizer) contactDisplayName(ctx context.Context, addresses ...types.JID) string {
	if normalizer.client == nil || normalizer.client.Store == nil || normalizer.client.Store.Contacts == nil {
		return ""
	}
	for _, address := range addresses {
		address = address.ToNonAD()
		if address.IsEmpty() {
			continue
		}
		contact, err := normalizer.client.Store.Contacts.GetContact(ctx, address)
		if err != nil {
			continue
		}
		for _, candidate := range []string{contact.PushName, contact.FullName, contact.BusinessName, contact.FirstName, contact.Username} {
			if name := boundedDisplayName(candidate); name != "" {
				return name
			}
		}
	}
	return ""
}

func (normalizer *messageNormalizer) extractInboundMentions(ctx context.Context, text string, mentioned []string) []conversation.IncomingMention {
	if len(mentioned) == 0 {
		return nil
	}
	result := make([]conversation.IncomingMention, 0, len(mentioned))
	seen := make(map[string]struct{}, len(mentioned))
	for _, raw := range mentioned {
		address, err := types.ParseJID(raw)
		if err != nil {
			continue
		}
		address = address.ToNonAD()
		token := "@" + address.User
		if address.User == "" || !mention.Contains(text, token) {
			continue
		}
		if _, exists := seen[token]; exists {
			continue
		}
		if normalizer.mentionsOwnAccount([]string{raw}) {
			seen[token] = struct{}{}
			result = append(result, conversation.IncomingMention{Token: token, Bot: true})
			if len(result) == conversation.MaxMentions {
				break
			}
			continue
		}

		target, ok := lidAddress(address)
		if !ok && address.Server == types.DefaultUserServer && normalizer.client != nil && normalizer.client.Store != nil && normalizer.client.Store.LIDs != nil {
			target, err = normalizer.client.Store.LIDs.GetLIDForPN(ctx, address)
			ok = err == nil && !target.IsEmpty()
		}
		if !ok {
			continue
		}
		seen[token] = struct{}{}
		result = append(result, conversation.IncomingMention{
			Token: token, TargetLID: mustLID(target),
			DisplayName: normalizer.contactDisplayName(ctx, address, target),
		})
		if len(result) == conversation.MaxMentions {
			break
		}
	}
	return result
}

func boundedDisplayName(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= agent.MaxDisplayNameBytes {
		return value
	}
	value = value[:agent.MaxDisplayNameBytes]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return strings.TrimSpace(value)
}

func phoneAddress(primary types.JID, alternatives ...types.JID) types.JID {
	if primary.Server == types.DefaultUserServer || primary.Server == types.HostedServer {
		return primary.ToNonAD()
	}
	for _, alternative := range alternatives {
		alternative = alternative.ToNonAD()
		if alternative.Server == types.DefaultUserServer || alternative.Server == types.HostedServer {
			return alternative
		}
	}
	return primary.ToNonAD()
}

func lidAddress(addresses ...types.JID) (types.JID, bool) {
	for _, address := range addresses {
		address = address.ToNonAD()
		if (address.Server == types.HiddenUserServer || address.Server == types.HostedLIDServer) && address.User != "" {
			return address, true
		}
	}
	return types.EmptyJID, false
}

func mustLID(address types.JID) identity.LID {
	lid, err := identity.ParseLID(address.ToNonAD().String())
	if err != nil {
		panic("validated WhatsApp LID could not be parsed")
	}
	return lid
}

func jidString(address types.JID) string {
	if address.IsEmpty() {
		return ""
	}
	return address.ToNonAD().String()
}

func (normalizer *messageNormalizer) mentionsOwnAccount(mentioned []string) bool {
	if len(mentioned) == 0 || normalizer.client == nil || normalizer.client.Store == nil {
		return false
	}
	own := make(map[string]struct{}, 2)
	if normalizer.client.Store.ID != nil {
		own[normalizer.client.Store.ID.ToNonAD().String()] = struct{}{}
	}
	if !normalizer.client.Store.LID.IsEmpty() {
		own[normalizer.client.Store.LID.ToNonAD().String()] = struct{}{}
	}
	for _, raw := range mentioned {
		if normalized, err := normalizeAddress(raw); err == nil {
			if _, exists := own[normalized]; exists {
				return true
			}
		}
	}
	return false
}
