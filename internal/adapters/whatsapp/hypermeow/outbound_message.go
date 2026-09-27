package hypermeow

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	whatsmeow "github.com/polymorfa/hypermeow"
	waBinary "github.com/polymorfa/hypermeow/binary"
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

// buttonsMessage builds a native-flow message with one quick_reply button per
// request button. It is wrapped in viewOnceMessage like WhatsApp's own clients
// send it; hypermeow adds the biz node that makes the buttons render.
func buttonsMessage(request action.SendButtonsRequest) (*waE2E.Message, error) {
	if strings.TrimSpace(request.Text) == "" || len(request.Buttons) == 0 || len(request.Buttons) > action.MaxButtons {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "build WhatsApp buttons", errors.New("text and 1 to 10 buttons are required"))
	}
	buttons := make([]*waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton, 0, len(request.Buttons))
	for _, button := range request.Buttons {
		if strings.TrimSpace(button.ID) == "" || strings.TrimSpace(button.Label) == "" {
			return nil, agent.NewError(agent.ErrorInvalidArgument, "build WhatsApp buttons", errors.New("every button needs an ID and a label"))
		}
		params, err := json.Marshal(map[string]string{"display_text": button.Label, "id": button.ID})
		if err != nil {
			return nil, agent.NewError(agent.ErrorInternal, "build WhatsApp buttons", err)
		}
		buttons = append(buttons, &waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{
			Name:             proto.String("quick_reply"),
			ButtonParamsJSON: proto.String(string(params)),
		})
	}
	return wrapNativeFlow(&waE2E.Message{InteractiveMessage: &waE2E.InteractiveMessage{
		Header: &waE2E.InteractiveMessage_Header{HasMediaAttachment: proto.Bool(false)},
		Body:   &waE2E.InteractiveMessage_Body{Text: proto.String(request.Text)},
		InteractiveMessage: &waE2E.InteractiveMessage_NativeFlowMessage_{
			NativeFlowMessage: &waE2E.InteractiveMessage_NativeFlowMessage{Buttons: buttons, MessageVersion: proto.Int32(1)},
		},
	}}), nil
}

// nativeFlowSend prepares a native-flow (button) message and the stanza nodes
// to send it with. Other messages are returned unchanged with no extras.
//
// hypermeow tags every native-flow message it recognises with a
// business-hosting biz node (actual_actors, host_storage, quality_control)
// that WhatsApp rejects from an ordinary account with 405, and it has no
// option to change that node. It only looks for buttons inside viewOnce and
// ephemeral wrappers, so the message goes out inside documentWithCaption,
// a generic future-proof envelope clients unwrap, and the nodes the original
// Baileys bot sent successfully are passed instead:
//
//	biz > interactive(type=native_flow, v=1) > native_flow(name=mixed, v=9)
//	bot(biz_bot=1), outside groups
func nativeFlowSend(message *waE2E.Message, to types.JID) (*waE2E.Message, []whatsmeow.SendRequestExtra) {
	wrapped := wrapNativeFlow(message)
	if wrapped.GetViewOnceMessage().GetMessage().GetInteractiveMessage().GetNativeFlowMessage() == nil {
		return message, nil
	}
	nodes := []waBinary.Node{{
		Tag: "biz",
		Content: []waBinary.Node{{
			Tag:   "interactive",
			Attrs: waBinary.Attrs{"type": "native_flow", "v": "1"},
			Content: []waBinary.Node{{
				Tag:   "native_flow",
				Attrs: waBinary.Attrs{"v": "9", "name": "mixed"},
			}},
		}},
	}}
	if to.Server != types.GroupServer {
		nodes = append(nodes, waBinary.Node{Tag: "bot", Attrs: waBinary.Attrs{"biz_bot": "1"}})
	}
	outgoing := &waE2E.Message{DocumentWithCaptionMessage: &waE2E.FutureProofMessage{Message: wrapped}}
	return outgoing, []whatsmeow.SendRequestExtra{{AdditionalNodes: &nodes}}
}

// wrapNativeFlow puts a bare native-flow InteractiveMessage inside the
// viewOnce envelope with device-list metadata, which is the shape WhatsApp
// clients render. Command buttons and broadcast payloads both go through it,
// so a pasted {"interactiveMessage": ...} is sent the same way. Any other
// message is returned unchanged.
func wrapNativeFlow(message *waE2E.Message) *waE2E.Message {
	if message.GetInteractiveMessage().GetNativeFlowMessage() == nil {
		return message
	}
	inner := proto.Clone(message).(*waE2E.Message)
	if inner.MessageContextInfo == nil {
		inner.MessageContextInfo = &waE2E.MessageContextInfo{}
	}
	if inner.MessageContextInfo.DeviceListMetadata == nil {
		inner.MessageContextInfo.DeviceListMetadata = &waE2E.DeviceListMetadata{}
	}
	if inner.MessageContextInfo.DeviceListMetadataVersion == nil {
		inner.MessageContextInfo.DeviceListMetadataVersion = proto.Int32(2)
	}
	return &waE2E.Message{ViewOnceMessage: &waE2E.FutureProofMessage{Message: inner}}
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
