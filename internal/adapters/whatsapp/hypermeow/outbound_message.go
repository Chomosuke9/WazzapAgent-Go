package hypermeow

import (
	"context"
	"encoding/json"
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
	if !request.QuotedMessageID.IsZero() || len(mentionedJIDs) > 0 || rendered.nonJID > 0 || rendered.admins {
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
	return &waE2E.Message{ViewOnceMessage: &waE2E.FutureProofMessage{Message: &waE2E.Message{
		MessageContextInfo: &waE2E.MessageContextInfo{
			DeviceListMetadata:        &waE2E.DeviceListMetadata{},
			DeviceListMetadataVersion: proto.Int32(2),
		},
		InteractiveMessage: &waE2E.InteractiveMessage{
			Body: &waE2E.InteractiveMessage_Body{Text: proto.String(request.Text)},
			InteractiveMessage: &waE2E.InteractiveMessage_NativeFlowMessage_{
				NativeFlowMessage: &waE2E.InteractiveMessage_NativeFlowMessage{Buttons: buttons, MessageVersion: proto.Int32(1)},
			},
		},
	}}}, nil
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
