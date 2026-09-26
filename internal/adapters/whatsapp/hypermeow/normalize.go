package hypermeow

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/types"
	"github.com/polymorfa/hypermeow/types/events"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/mention"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/policy"
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
	if text, _ := buttonReply(event.Message); event.Message.GetConversation() == "" && event.Message.GetExtendedTextMessage().GetText() == "" && event.Message.GetStickerMessage() == nil && text == "" {
		return "unsupported_content"
	}
	return "invalid_metadata"
}

func (normalizer *messageNormalizer) normalizeMessage(ctx context.Context, event *events.Message) (conversation.IncomingCandidate, bool) {
	if event == nil || event.Message == nil || event.IsEdit {
		return conversation.IncomingCandidate{}, false
	}
	text := event.Message.GetConversation()
	extended := event.Message.GetExtendedTextMessage()
	if text == "" && extended != nil {
		text = extended.GetText()
	}
	contextInfo := (*waE2E.ContextInfo)(nil)
	if extended != nil {
		contextInfo = extended.GetContextInfo()
	}
	if text == "" {
		if sticker := event.Message.GetStickerMessage(); sticker != nil {
			// Part 2 remains text-only for the model, but the canonical transcript
			// must not lose visible group chronology when someone sends a sticker.
			text = "【sticker】"
			contextInfo = sticker.GetContextInfo()
		}
	}
	if text == "" {
		// A button tap arrives as the tapped button's ID, so a
		// "/command args" button re-enters its command like typed text.
		text, contextInfo = buttonReply(event.Message)
	}
	if text == "" {
		return conversation.IncomingCandidate{}, false
	}
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

// buttonReply extracts a tap on a quick-reply, buttons, template, or list
// message. Slash-command IDs become the message text; any other ID yields the
// label the user saw, so the conversation reads naturally.
func buttonReply(message *waE2E.Message) (string, *waE2E.ContextInfo) {
	pick := func(id, label string) string {
		if strings.HasPrefix(id, "/") {
			return id
		}
		if strings.TrimSpace(label) != "" {
			return label
		}
		return id
	}
	switch {
	case message.GetInteractiveResponseMessage() != nil:
		response := message.GetInteractiveResponseMessage()
		var params struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal([]byte(response.GetNativeFlowResponseMessage().GetParamsJSON()), &params)
		return pick(params.ID, response.GetBody().GetText()), response.GetContextInfo()
	case message.GetButtonsResponseMessage() != nil:
		response := message.GetButtonsResponseMessage()
		return pick(response.GetSelectedButtonID(), response.GetSelectedDisplayText()), response.GetContextInfo()
	case message.GetTemplateButtonReplyMessage() != nil:
		response := message.GetTemplateButtonReplyMessage()
		return pick(response.GetSelectedID(), response.GetSelectedDisplayText()), response.GetContextInfo()
	case message.GetListResponseMessage() != nil:
		response := message.GetListResponseMessage()
		return pick(response.GetSingleSelectReply().GetSelectedRowID(), response.GetTitle()), response.GetContextInfo()
	default:
		return "", nil
	}
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
