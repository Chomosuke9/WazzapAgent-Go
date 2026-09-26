package hypermeow

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/types"
	"google.golang.org/protobuf/proto"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/action"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

var outboundMentionPattern = regexp.MustCompile(`@([^@()\r\n]+?)\s*\(([0-9A-Za-z]{3,16})\)`)

func (adapter *Adapter) textMessage(ctx context.Context, request action.SendTextRequest, address string, target types.JID) (*waE2E.Message, error) {
	renderedText, mentionedJIDs, nonJIDMentions := renderOutboundMentions(request.Text, target, adapter.ownJID(), adapter.mentionResolver(ctx, request.Key))
	message := &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: proto.String(renderedText),
		},
	}
	contextInfo := &waE2E.ContextInfo{MentionedJID: mentionedJIDs}
	if nonJIDMentions > 0 {
		contextInfo.NonJIDMentions = proto.Uint32(nonJIDMentions)
	}

	// Resolve the model-selected target before sending. An unresolved target must
	// not silently turn an explicit reply into an ordinary message.
	if !request.QuotedMessageID.IsZero() {
		quotedChat, quotedProviderID, quotedSender, _, resolveErr := adapter.targets.ResolveMessageTarget(ctx, request.Key, request.QuotedMessageID)
		if resolveErr != nil {
			return nil, resolveErr
		}
		if quotedChat != address || quotedProviderID == "" {
			return nil, agent.NewError(agent.ErrorIntegrityFailure, "resolve WhatsApp reply target", errors.New("quoted target does not belong to destination chat"))
		}
		contextInfo.StanzaID = proto.String(quotedProviderID)
		contextInfo.RemoteJID = proto.String(target.ToNonAD().String())
		if quotedSender != "" {
			contextInfo.Participant = proto.String(quotedSender)
		} else if own := adapter.ownJID(); !own.IsEmpty() {
			// Outbound actions have no human sender row. Group replies to the
			// bot's own messages still need the original sender JID.
			contextInfo.Participant = proto.String(own.String())
		}
	}
	if !request.QuotedMessageID.IsZero() || len(mentionedJIDs) > 0 || nonJIDMentions > 0 {
		message.ExtendedTextMessage.ContextInfo = contextInfo
	}
	return message, nil
}

// ownJID is the paired device's phone JID, or empty before pairing completes.
func (adapter *Adapter) ownJID() types.JID {
	if adapter.client == nil || adapter.client.Store == nil || adapter.client.Store.ID == nil {
		return types.EmptyJID
	}
	return adapter.client.Store.ID.ToNonAD()
}

// mentionResolver maps a model-visible sender ref to the member's LID JID
// through the durable target store.
func (adapter *Adapter) mentionResolver(ctx context.Context, key agent.Key) func(identity.SenderRef) (types.JID, bool) {
	return func(ref identity.SenderRef) (types.JID, bool) {
		if adapter.targets == nil {
			return types.EmptyJID, false
		}
		lid, err := adapter.targets.ResolveLID(ctx, key, ref)
		if err != nil {
			return types.EmptyJID, false
		}
		jid, err := types.ParseJID(lid.String())
		if err != nil || jid.IsEmpty() {
			return types.EmptyJID, false
		}
		return jid, true
	}
}

// renderOutboundMentions rewrites the model's "@Name (ref)" markup into
// WhatsApp wire mentions. "all" becomes a non-JID group mention, "bot" the
// account itself, and any other ref is resolved to a member JID; unresolved
// refs degrade to plain "@Name" text rather than failing the send.
func renderOutboundMentions(rawText string, target, bot types.JID, resolve func(identity.SenderRef) (types.JID, bool)) (string, []string, uint32) {
	matches := outboundMentionPattern.FindAllStringSubmatchIndex(rawText, -1)
	if len(matches) == 0 {
		return rawText, nil, 0
	}
	var rendered strings.Builder
	mentioned := make([]string, 0, len(matches))
	seen := make(map[string]struct{}, len(matches))
	var nonJIDMentions uint32
	cursor := 0
	addMention := func(jid types.JID) {
		if jid.IsEmpty() {
			return
		}
		value := jid.ToNonAD().String()
		if _, exists := seen[value]; exists {
			return
		}
		seen[value] = struct{}{}
		mentioned = append(mentioned, value)
	}
	for _, match := range matches {
		rendered.WriteString(rawText[cursor:match[0]])
		name := strings.TrimSpace(rawText[match[2]:match[3]])
		value := strings.ToLower(strings.TrimSpace(rawText[match[4]:match[5]]))
		replacement := "@" + name
		switch value {
		case "all":
			replacement = "@all"
			if target.Server == types.GroupServer {
				nonJIDMentions = 1
			}
		case "bot":
			if !bot.IsEmpty() {
				addMention(bot)
				replacement = "@" + bot.User
			}
		default:
			ref, err := identity.ParseSenderRef(value)
			if err == nil {
				if jid, ok := resolve(ref); ok {
					addMention(jid)
					replacement = "@" + jid.User
				}
			}
		}
		rendered.WriteString(replacement)
		cursor = match[1]
	}
	rendered.WriteString(rawText[cursor:])
	return rendered.String(), mentioned, nonJIDMentions
}
