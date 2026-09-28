package hypermeow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/types"
	"google.golang.org/protobuf/proto"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/action"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

var outboundMentionPattern = regexp.MustCompile(`@([^@()\r\n]+?)\s*\(([0-9A-Za-z]{3,16})\)`)

func (adapter *Adapter) textMessage(ctx context.Context, request action.SendTextRequest, address string, target types.JID) (*waE2E.Message, error) {
	rendered, err := renderOutboundMentions(request.Text, target, adapter.ownJID(), adapter.mentionResolver(ctx, request.Key), adapter.groupAdmins(ctx, target))
	if err != nil {
		return nil, err
	}
	mentionedJIDs := rendered.jids
	message := &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: proto.String(rendered.text),
		},
	}
	contextInfo := &waE2E.ContextInfo{MentionedJID: mentionedJIDs}
	if rendered.nonJID > 0 {
		contextInfo.NonJIDMentions = proto.Uint32(rendered.nonJID)
	}
	if rendered.admins {
		// WhatsApp shows a mention of the group's own JID with this subject
		// as "@admin"; the admins themselves are in MentionedJID.
		contextInfo.GroupMentions = []*waE2E.GroupMention{{
			GroupJID: proto.String(target.ToNonAD().String()), GroupSubject: proto.String("admin"),
		}}
	}

	quote, err := adapter.quoteContext(ctx, request.Key, address, target, request.QuotedMessageID)
	if err != nil {
		return nil, err
	}
	if quote != nil {
		contextInfo.StanzaID, contextInfo.RemoteJID, contextInfo.Participant = quote.StanzaID, quote.RemoteJID, quote.Participant
	}
	if quote != nil || len(mentionedJIDs) > 0 || rendered.nonJID > 0 || rendered.admins {
		message.ExtendedTextMessage.ContextInfo = contextInfo
	}
	return message, nil
}

// quoteContext is the context info that makes a message reply to quoted, or
// nil when quoted is zero. An unresolved target fails the send rather than
// silently turning an explicit reply into an ordinary message.
func (adapter *Adapter) quoteContext(ctx context.Context, key agent.Key, address string, target types.JID, quoted identity.MessageID) (*waE2E.ContextInfo, error) {
	if quoted.IsZero() {
		return nil, nil
	}
	quotedChat, quotedProviderID, quotedSender, _, err := adapter.targets.ResolveMessageTarget(ctx, key, quoted)
	if err != nil {
		return nil, err
	}
	if quotedChat != address || quotedProviderID == "" {
		return nil, agent.NewError(agent.ErrorIntegrityFailure, "resolve WhatsApp reply target", errors.New("quoted target does not belong to destination chat"))
	}
	contextInfo := &waE2E.ContextInfo{StanzaID: proto.String(quotedProviderID), RemoteJID: proto.String(target.ToNonAD().String())}
	if quotedSender != "" {
		contextInfo.Participant = proto.String(quotedSender)
	} else if own := adapter.ownJID(); !own.IsEmpty() {
		// Outbound actions have no human sender row. Group replies to the
		// bot's own messages still need the original sender JID.
		contextInfo.Participant = proto.String(own.String())
	}
	return contextInfo, nil
}

// buttonsMessage builds a native-flow message with one quick_reply button per
// request button.
func buttonsMessage(request action.SendButtonsRequest) (*waE2E.Message, error) {
	if strings.TrimSpace(request.Text) == "" || len(request.Buttons) == 0 || len(request.Buttons) > action.MaxButtons {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "build WhatsApp buttons", errors.New("text and 1 to 10 buttons are required"))
	}
	buttons := make([]*waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton, 0, len(request.Buttons))
	for _, button := range request.Buttons {
		if strings.TrimSpace(button.ID) == "" || strings.TrimSpace(button.Label) == "" {
			return nil, agent.NewError(agent.ErrorInvalidArgument, "build WhatsApp buttons", errors.New("every button needs an ID and a label"))
		}
		buttons = append(buttons, nativeFlowButton("quick_reply", map[string]string{"display_text": button.Label, "id": button.ID}))
	}
	return nativeFlowMessage(nativeFlowHeader{}, request.Text, nil, buttons), nil
}

// quizMessage turns a built text message (mentions and quote already
// resolved) into the same text under the quiz's header, with one quick_reply
// button per choice. The button IDs do not start with "/", so a tap arrives
// as the choice's label.
func quizMessage(text *waE2E.ExtendedTextMessage, quiz action.Quiz) *waE2E.Message {
	buttons := make([]*waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton, 0, len(quiz.Choices))
	for index, choice := range quiz.Choices {
		buttons = append(buttons, nativeFlowButton("quick_reply", map[string]string{"display_text": choice, "id": fmt.Sprintf("quiz:%d", index+1)}))
	}
	header := nativeFlowHeader{title: quiz.Title, subtitle: quiz.Subtitle, footer: quiz.Footer}
	return nativeFlowMessage(header, text.GetText(), text.GetContextInfo(), buttons)
}

// quizFallbackText is the quiz as plain text, for when buttons are rejected.
func quizFallbackText(text string, choices []string) string {
	var builder strings.Builder
	builder.WriteString(strings.TrimSpace(text))
	builder.WriteString("\n")
	for index, choice := range choices {
		fmt.Fprintf(&builder, "\n%d. %s", index+1, choice)
	}
	builder.WriteString("\n\n_Reply with your choice._")
	return builder.String()
}

// copyCodeMessage is a cta_copy button carrying code. Like the legacy bot, it
// quotes a synthetic message holding a short preview of the code, so the
// bubble shows what the button copies.
func copyCodeMessage(code string, target, own types.JID) (*waE2E.Message, error) {
	if strings.TrimSpace(code) == "" {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "build WhatsApp copy button", errors.New("code is required"))
	}
	contextInfo := &waE2E.ContextInfo{
		StanzaID:      proto.String(fmt.Sprintf("CPY%X", time.Now().UnixNano())),
		RemoteJID:     proto.String(target.ToNonAD().String()),
		QuotedMessage: &waE2E.Message{Conversation: proto.String(codePreview(code))},
	}
	if !own.IsEmpty() {
		contextInfo.Participant = proto.String(own.String())
	}
	button := nativeFlowButton("cta_copy", map[string]string{"display_text": "Copy code", "copy_code": code})
	return nativeFlowMessage(nativeFlowHeader{}, "", contextInfo, []*waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{button}), nil
}

// codePreview is the code on one line, cut to 120 characters.
func codePreview(code string) string {
	preview := []rune(strings.Join(strings.Fields(code), " "))
	if len(preview) > 120 {
		return string(preview[:119]) + "…"
	}
	return string(preview)
}

func plainTextMessage(text string) *waE2E.Message {
	return &waE2E.Message{Conversation: proto.String(text)}
}

func nativeFlowButton(name string, params map[string]string) *waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton {
	// Marshalling a map of strings cannot fail.
	encoded, _ := json.Marshal(params)
	return &waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{
		Name:             proto.String(name),
		ButtonParamsJSON: proto.String(string(encoded)),
	}
}

// invisibleText fills an interactive message's empty text fields. WhatsApp
// only renders native-flow buttons when the body, header title, subtitle and
// footer are all present, and it treats an empty or whitespace-only string
// as absent. U+3164 HANGUL FILLER is not whitespace but draws as a blank.
const invisibleText = "\u3164"

// nativeFlowHeader is the text around a native-flow message's body. Empty
// fields are sent as invisibleText.
type nativeFlowHeader struct {
	title, subtitle, footer string
}

// nativeFlowMessage is a top-level interactive message with native-flow
// buttons, in the shape Rey confirmed WhatsApp accepts and renders: body,
// footer and a media-less header with title and subtitle all set. The
// trailing nameless button is the 405 trick: hypermeow names its biz node
// after the buttons ("quick_reply", "cta_copy"), and with a nameless one
// present it falls back to "mixed", which the server accepts.
func nativeFlowMessage(header nativeFlowHeader, body string, contextInfo *waE2E.ContextInfo, buttons []*waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton) *waE2E.Message {
	buttons = append(buttons, &waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{Name: proto.String("")})
	return &waE2E.Message{InteractiveMessage: &waE2E.InteractiveMessage{
		Header: &waE2E.InteractiveMessage_Header{
			Title:              proto.String(visibleOrInvisible(header.title)),
			Subtitle:           proto.String(visibleOrInvisible(header.subtitle)),
			HasMediaAttachment: proto.Bool(false),
		},
		Body:        &waE2E.InteractiveMessage_Body{Text: proto.String(visibleOrInvisible(body))},
		Footer:      &waE2E.InteractiveMessage_Footer{Text: proto.String(visibleOrInvisible(header.footer))},
		ContextInfo: contextInfo,
		InteractiveMessage: &waE2E.InteractiveMessage_NativeFlowMessage_{
			NativeFlowMessage: &waE2E.InteractiveMessage_NativeFlowMessage{Buttons: buttons},
		},
	}}
}

// visibleOrInvisible is text, or invisibleText when text is blank.
func visibleOrInvisible(text string) string {
	if strings.TrimSpace(text) == "" {
		return invisibleText
	}
	return text
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

// groupAdmins lists the admins of a group chat from the synchronized group
// snapshot. It fails while the snapshot is not ready, so the send is tried
// again later instead of showing "@admin" that notifies nobody.
func (adapter *Adapter) groupAdmins(ctx context.Context, chat types.JID) func() ([]types.JID, error) {
	return func() ([]types.JID, error) {
		info, err := adapter.readGroupInfo(ctx, chat)
		if err != nil {
			return nil, err
		}
		admins := make([]types.JID, 0, 4)
		for _, participant := range info.Participants {
			if !participant.IsAdmin && !participant.IsSuperAdmin {
				continue
			}
			if !participant.LID.IsEmpty() {
				admins = append(admins, participant.LID)
			} else {
				admins = append(admins, participant.JID)
			}
		}
		return admins, nil
	}
}

type renderedMentions struct {
	text   string
	jids   []string
	nonJID uint32
	// admins is set when the text mentions the group's admins.
	admins bool
}

// renderOutboundMentions rewrites the model's "@Name (ref)" markup into
// WhatsApp wire mentions. "all" becomes a non-JID group mention, "admin" a
// group mention that tags every admin listed by admins, "bot" the account
// itself, and any other ref is resolved to a member JID; unresolved refs
// degrade to plain "@Name" text rather than failing the send.
func renderOutboundMentions(rawText string, target, bot types.JID, resolve func(identity.SenderRef) (types.JID, bool), admins func() ([]types.JID, error)) (renderedMentions, error) {
	matches := outboundMentionPattern.FindAllStringSubmatchIndex(rawText, -1)
	if len(matches) == 0 {
		return renderedMentions{text: rawText}, nil
	}
	var rendered strings.Builder
	result := renderedMentions{jids: make([]string, 0, len(matches))}
	seen := make(map[string]struct{}, len(matches))
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
		result.jids = append(result.jids, value)
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
				result.nonJID = 1
			}
		case "admin":
			replacement = "@admin"
			if target.Server == types.GroupServer {
				// The text carries the full group JID; WhatsApp replaces it
				// with the group mention's subject.
				replacement = "@" + target.ToNonAD().String()
				result.admins = true
				list, err := admins()
				if err != nil {
					return renderedMentions{}, err
				}
				for _, admin := range list {
					addMention(admin)
				}
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
	result.text = rendered.String()
	return result, nil
}
