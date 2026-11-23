package hypermeow

import (
	"testing"

	"github.com/polymorfa/hypermeow/proto/waAICommon"
	"github.com/polymorfa/hypermeow/proto/waAICommonDeprecated"
	"github.com/polymorfa/hypermeow/proto/waCommon"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/types"
	"google.golang.org/protobuf/proto"
)

func TestMetaAITextReadsEveryReplyShape(t *testing.T) {
	text := waAICommonDeprecated.AIRichResponseSubMessageType_AI_RICH_RESPONSE_TEXT
	code := waAICommonDeprecated.AIRichResponseSubMessageType_AI_RICH_RESPONSE_CODE
	rich := &waE2E.Message{RichResponseMessage: &waE2E.AIRichResponseMessage{
		Submessages: []*waAICommonDeprecated.AIRichResponseSubMessage{
			{MessageType: &text, MessageText: proto.String("Here you go:")},
			{MessageType: &code, CodeMetadata: &waAICommonDeprecated.AIRichResponseCodeMetadata{
				CodeBlocks: []*waAICommonDeprecated.AIRichResponseCodeMetadata_AIRichResponseCodeBlock{
					{CodeContent: proto.String("print(")}, {CodeContent: proto.String("1)")},
				},
			}},
		},
	}}
	unified := &waE2E.Message{RichResponseMessage: &waE2E.AIRichResponseMessage{
		UnifiedResponse: &waAICommon.AIRichResponseUnifiedResponse{
			Data: []byte(`{"sections":[{"view_model":{"primitive":{"text":"Hi! Lagi santai?"}}}]}`),
		},
	}}
	edit := &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type:          waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(),
		Key:           &waCommon.MessageKey{ID: proto.String("FIRST")},
		EditedMessage: &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("Your image is ready")}},
	}}
	for name, test := range map[string]struct {
		message *waE2E.Message
		want    string
	}{
		"conversation": {&waE2E.Message{Conversation: proto.String("plain")}, "plain"},
		"rich":         {rich, "Here you go:\n\n```\nprint(1)\n```"},
		"unified":      {unified, "Hi! Lagi santai?"},
		"edit":         {edit, "Your image is ready"},
	} {
		if got := metaAIText(metaAIContent(test.message)); got != test.want {
			t.Errorf("%s: text = %q, want %q", name, got, test.want)
		}
	}
}

func TestOnlyMetaAIIsMetaAI(t *testing.T) {
	if !isMetaAI(types.NewMetaAIJID) || !isMetaAI(types.MetaAIJID) {
		t.Fatal("Meta AI's JIDs are not recognized")
	}
	if isMetaAI(types.NewJID("867051314767697", types.BotServer)) {
		t.Fatal("another bot was taken for Meta AI")
	}
}
