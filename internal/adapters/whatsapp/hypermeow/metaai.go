package hypermeow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	whatsmeow "github.com/polymorfa/hypermeow"
	"github.com/polymorfa/hypermeow/proto/waAICommonDeprecated"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/types"
	"github.com/polymorfa/hypermeow/types/events"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// metaAIJID is Meta AI's chat. Its phone-number JID (types.MetaAIJID) does
// not resolve to a LID, so messages go to the bot JID.
var metaAIJID = types.NewMetaAIJID

// MetaAIMessage is one message or edit from Meta AI, as the sub-agent reads
// it. See subagent.MetaAIEvent.
type MetaAIMessage struct {
	ReplyID      string
	Final        bool
	ImagePending bool
	Text         string
	Image        []byte
	ImageMime    string
}

// SetMetaAIHandler routes Meta AI's messages to handler. Without one they
// are dropped. Messages from bots never reach the chat pipeline either way.
// handler must not block: it runs on the WhatsApp event goroutine.
func (adapter *Adapter) SetMetaAIHandler(handler func(MetaAIMessage)) {
	if handler == nil {
		adapter.metaAI.Store(nil)
		return
	}
	adapter.metaAI.Store(&handler)
}

// SendMetaAIText sends text to Meta AI.
func (adapter *Adapter) SendMetaAIText(ctx context.Context, text string) error {
	return adapter.sendMetaAI(ctx, "send Meta AI message", &waE2E.Message{Conversation: proto.String(text)})
}

// SendMetaAIImage uploads an image and sends it to Meta AI with caption.
func (adapter *Adapter) SendMetaAIImage(ctx context.Context, data []byte, mimeType, caption string) error {
	const operation = "send Meta AI image"
	if !adapter.Ready() {
		return agent.NewError(agent.ErrorNotReady, operation, errors.New("account is not connected"))
	}
	if int64(len(data)) > maxFileBytes {
		return agent.NewError(agent.ErrorInvalidArgument, operation, errMediaTooLarge)
	}
	uploadCtx, cancel := context.WithTimeout(ctx, fileTransferTimeout)
	defer cancel()
	uploaded, err := adapter.client.Upload(uploadCtx, data, whatsmeow.MediaImage)
	if err != nil {
		return nativeEffectError(uploadCtx, "upload Meta AI image", err)
	}
	if mimeType == "" {
		mimeType = "image/jpeg"
	}
	image := &waE2E.ImageMessage{
		URL: proto.String(uploaded.URL), DirectPath: proto.String(uploaded.DirectPath), MediaKey: uploaded.MediaKey,
		FileEncSHA256: uploaded.FileEncSHA256, FileSHA256: uploaded.FileSHA256, FileLength: proto.Uint64(uploaded.FileLength),
		Mimetype: proto.String(mimeType),
	}
	if caption != "" {
		image.Caption = proto.String(caption)
	}
	return adapter.sendMetaAI(ctx, operation, &waE2E.Message{ImageMessage: image})
}

func (adapter *Adapter) sendMetaAI(ctx context.Context, operation string, message *waE2E.Message) error {
	if !adapter.Ready() {
		return agent.NewError(agent.ErrorNotReady, operation, errors.New("account is not connected"))
	}
	sendCtx, cancel := context.WithTimeout(ctx, adapter.sendTimeout)
	defer cancel()
	if _, err := adapter.client.SendMessage(sendCtx, metaAIJID, message); err != nil {
		return nativeEffectError(sendCtx, operation, err)
	}
	return nil
}

// routeMetaAI takes a message from a bot out of the chat pipeline and hands
// Meta AI's to the handler. It reports whether the message was a bot's.
func (adapter *Adapter) routeMetaAI(event *events.Message) bool {
	if event == nil || !(event.Info.Chat.IsBot() || event.Info.Sender.IsBot()) {
		return false
	}
	pointer := adapter.metaAI.Load()
	if pointer == nil || event.Info.IsFromMe || !isMetaAI(event.Info.Sender) {
		return true
	}
	handler := *pointer
	message := metaAIContent(event.Message)
	adapter.logger.Debug("Meta AI message", "id", event.Info.ID, "edit", event.Info.MsgBotInfo.EditType,
		"edit_target", event.Info.MsgBotInfo.EditTargetID, "fields", setMessageFields(message))
	replyID := string(event.Info.ID)
	final := true
	switch event.Info.MsgBotInfo.EditType {
	case types.EditTypeFirst:
		final = false
	case types.EditTypeInner:
		final = false
		replyID = string(event.Info.MsgBotInfo.EditTargetID)
	case types.EditTypeLast:
		replyID = string(event.Info.MsgBotInfo.EditTargetID)
	}
	if key := event.Message.GetProtocolMessage().GetKey(); key.GetID() != "" && event.Message.GetProtocolMessage().GetEditedMessage() != nil {
		replyID = key.GetID()
	}
	text := metaAIText(message)
	image := message.GetImageMessage()
	if image == nil || image.GetDirectPath() == "" {
		handler(MetaAIMessage{ReplyID: replyID, Final: final, Text: text})
		return true
	}
	if adapter.closed.Load() || adapter.rootCtx.Err() != nil {
		handler(MetaAIMessage{ReplyID: replyID, Final: true, Text: text})
		return true
	}
	// The download must not hold up the event goroutine. The reply is
	// announced now so the sub-agent waits for the image.
	handler(MetaAIMessage{ReplyID: replyID, Text: text, ImagePending: true})
	adapter.wait.Go(func() {
		ctx, cancel := context.WithTimeout(adapter.rootCtx, fileTransferTimeout)
		defer cancel()
		data, err := adapter.downloadBounded(ctx, image)
		if err != nil {
			adapter.logger.Warn("Meta AI image download failed", "error", err)
			data = nil
		}
		handler(MetaAIMessage{ReplyID: replyID, Final: true, Text: text, Image: data, ImageMime: image.GetMimetype()})
	})
	return true
}

func isMetaAI(sender types.JID) bool {
	user := sender.ToNonAD().User
	return user == types.NewMetaAIJID.User || user == types.MetaAIJID.User
}

// metaAIContent is the message itself, or for an edit the edited message.
func metaAIContent(message *waE2E.Message) *waE2E.Message {
	if edited := message.GetProtocolMessage().GetEditedMessage(); edited != nil {
		return edited
	}
	return message
}

// metaAIText is the whole text of one of Meta AI's messages. A rich reply
// keeps its text in submessages, or else in a JSON "unified response".
func metaAIText(message *waE2E.Message) string {
	if text := message.GetConversation(); text != "" {
		return text
	}
	if text := message.GetExtendedTextMessage().GetText(); text != "" {
		return text
	}
	if rich := message.GetRichResponseMessage(); rich != nil {
		var parts []string
		for _, sub := range rich.GetSubmessages() {
			switch sub.GetMessageType() {
			case waAICommonDeprecated.AIRichResponseSubMessageType_AI_RICH_RESPONSE_CODE:
				var code []string
				for _, block := range sub.GetCodeMetadata().GetCodeBlocks() {
					code = append(code, block.GetCodeContent())
				}
				if joined := strings.Join(code, ""); joined != "" {
					parts = append(parts, "```\n"+joined+"\n```")
				}
			default:
				if text := sub.GetMessageText(); text != "" {
					parts = append(parts, text)
				}
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n\n")
		}
		return unifiedResponseText(rich.GetUnifiedResponse().GetData())
	}
	return message.GetImageMessage().GetCaption()
}

// unifiedResponseText reads the markdown text out of a rich reply's unified
// response: {"sections":[{"view_model":{"primitive":{"text":...}}}]}.
func unifiedResponseText(data []byte) string {
	var response struct {
		Sections []struct {
			ViewModel struct {
				Primitive struct {
					Text string `json:"text"`
				} `json:"primitive"`
			} `json:"view_model"`
		} `json:"sections"`
	}
	if len(data) == 0 || json.Unmarshal(data, &response) != nil {
		return ""
	}
	var parts []string
	for _, section := range response.Sections {
		if text := section.ViewModel.Primitive.Text; text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// setMessageFields names the message's top-level fields that are set, for
// the debug log.
func setMessageFields(message *waE2E.Message) string {
	if message == nil {
		return ""
	}
	var names []string
	message.ProtoReflect().Range(func(field protoreflect.FieldDescriptor, _ protoreflect.Value) bool {
		names = append(names, string(field.Name()))
		return true
	})
	return strings.Join(names, ",")
}
