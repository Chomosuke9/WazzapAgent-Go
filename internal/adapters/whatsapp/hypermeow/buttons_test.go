package hypermeow

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	waBinary "github.com/polymorfa/hypermeow/binary"
	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/types"
	"github.com/polymorfa/hypermeow/types/events"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/action"
)

func TestButtonTapsBecomeMessageText(t *testing.T) {
	nativeFlow := func(id, body string) *waE2E.Message {
		return &waE2E.Message{InteractiveResponseMessage: &waE2E.InteractiveResponseMessage{
			Body: &waE2E.InteractiveResponseMessage_Body{Text: proto.String(body)},
			InteractiveResponseMessage: &waE2E.InteractiveResponseMessage_NativeFlowResponseMessage_{
				NativeFlowResponseMessage: &waE2E.InteractiveResponseMessage_NativeFlowResponseMessage{
					Name: proto.String("quick_reply"), ParamsJSON: proto.String(`{"id":"` + id + `"}`),
				},
			},
		}}
	}
	tests := []struct {
		name    string
		message *waE2E.Message
		want    string
	}{
		{name: "native flow command", message: nativeFlow("/trigger mention off", "Mention: turn off"), want: "/trigger mention off"},
		{name: "native flow other id shows label", message: nativeFlow("qz:1", "Answer A"), want: "Answer A"},
		{name: "legacy buttons", message: &waE2E.Message{ButtonsResponseMessage: &waE2E.ButtonsResponseMessage{
			Response: &waE2E.ButtonsResponseMessage_SelectedDisplayText{SelectedDisplayText: "Help"}, SelectedButtonID: proto.String("/help"),
		}}, want: "/help"},
		{name: "template", message: &waE2E.Message{TemplateButtonReplyMessage: &waE2E.TemplateButtonReplyMessage{
			SelectedID: proto.String("/info"), SelectedDisplayText: proto.String("Info"),
		}}, want: "/info"},
		{name: "list", message: &waE2E.Message{ListResponseMessage: &waE2E.ListResponseMessage{
			Title: proto.String("Reset"), SingleSelectReply: &waE2E.ListResponseMessage_SingleSelectReply{SelectedRowID: proto.String("/reset")},
		}}, want: "/reset"},
		{name: "native flow numeric id", message: &waE2E.Message{InteractiveResponseMessage: &waE2E.InteractiveResponseMessage{
			InteractiveResponseMessage: &waE2E.InteractiveResponseMessage_NativeFlowResponseMessage_{
				NativeFlowResponseMessage: &waE2E.InteractiveResponseMessage_NativeFlowResponseMessage{ParamsJSON: proto.String(`{"id":42}`)},
			},
		}}, want: "42"},
		{name: "plain text is not a tap", message: &waE2E.Message{Conversation: proto.String("hi")}, want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got, _ := buttonReply(test.message); got != test.want {
				t.Fatalf("buttonReply = %q, want %q", got, test.want)
			}
		})
	}

	adapter, _ := normalizationAdapter(t)
	chat := types.NewJID("15550000002", types.DefaultUserServer)
	sender := types.NewADJID("15550000001", 0, 7)
	setInboundGate(t, adapter, sender.ToNonAD().String(), chat.String())
	candidate, ok := adapter.normalizer.normalizeMessage(context.Background(), &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: sender, SenderAlt: types.NewJID("10000000001", types.HiddenUserServer)},
			ID:            types.MessageID("provider-tap-id"),
			Timestamp:     time.Now().UTC(),
		},
		Message: nativeFlow("/trigger mention off", "Mention: turn off"),
	})
	if !ok || candidate.Text != "/trigger mention off" {
		t.Fatalf("tap normalized = %v, %q", ok, candidate.Text)
	}
}

func TestButtonsMessageUsesNativeFlowQuickReplies(t *testing.T) {
	message, err := buttonsMessage(action.SendButtonsRequest{Text: "Pick", Buttons: []action.Button{{ID: "/trigger mention off", Label: "Mention: turn off"}}})
	if err != nil {
		t.Fatalf("build buttons: %v", err)
	}
	interactive := message.GetViewOnceMessage().GetMessage().GetInteractiveMessage()
	buttons := interactive.GetNativeFlowMessage().GetButtons()
	if interactive.GetBody().GetText() != "Pick" || len(buttons) != 1 || buttons[0].GetName() != "quick_reply" {
		t.Fatalf("interactive message = %v", interactive)
	}
	var params map[string]string
	if err := json.Unmarshal([]byte(buttons[0].GetButtonParamsJSON()), &params); err != nil || params["id"] != "/trigger mention off" || params["display_text"] != "Mention: turn off" {
		t.Fatalf("button params = %v, %v", params, err)
	}
	if _, err := buttonsMessage(action.SendButtonsRequest{Text: "Pick"}); err == nil {
		t.Fatal("buttons message without buttons was accepted")
	}
}

func TestWrapNativeFlowWrapsBareBroadcastPayload(t *testing.T) {
	payload := `{"interactiveMessage":{"nativeFlowMessage":{"buttons":[{"name":"quick_reply","buttonParamsJSON":"{\"display_text\":\"Pilihan 1\",\"id\":\"opt_1\"}"}]},"header":{"title":"Hasil"},"body":{"text":"Silakan pilih"}}}`
	message := &waE2E.Message{}
	if err := protojson.Unmarshal([]byte(payload), message); err != nil {
		t.Fatalf("parse payload: %v", err)
	}
	wrapped := wrapNativeFlow(message)
	inner := wrapped.GetViewOnceMessage().GetMessage()
	if inner.GetInteractiveMessage().GetHeader().GetTitle() != "Hasil" || len(inner.GetInteractiveMessage().GetNativeFlowMessage().GetButtons()) != 1 {
		t.Fatalf("wrapped message lost content: %v", wrapped)
	}
	if inner.GetMessageContextInfo().GetDeviceListMetadata() == nil || inner.GetMessageContextInfo().GetDeviceListMetadataVersion() != 2 {
		t.Fatalf("wrapped message has no device-list metadata: %v", wrapped)
	}
	if message.GetViewOnceMessage() != nil || message.GetMessageContextInfo() != nil {
		t.Fatal("wrapNativeFlow modified its input")
	}
	if again := wrapNativeFlow(wrapped); again != wrapped {
		t.Fatal("an already wrapped message was wrapped twice")
	}
	text := &waE2E.Message{Conversation: proto.String("hi")}
	if wrapNativeFlow(text) != text {
		t.Fatal("a plain text message was wrapped")
	}
}

func TestNativeFlowSendReplacesHypermeowBizNode(t *testing.T) {
	message, err := buttonsMessage(action.SendButtonsRequest{Text: "Pick", Buttons: []action.Button{{ID: "/test-button a", Label: "A"}}})
	if err != nil {
		t.Fatalf("build buttons: %v", err)
	}
	user := types.NewJID("15550000001", types.DefaultUserServer)
	outgoing, extra := nativeFlowSend(message, user)
	// hypermeow only adds its own biz node for buttons it finds directly or
	// inside viewOnce/ephemeral wrappers, so the top level must be neither.
	if outgoing.GetViewOnceMessage() != nil || outgoing.GetEphemeralMessage() != nil || outgoing.GetInteractiveMessage() != nil {
		t.Fatalf("buttons are still visible to hypermeow's detection: %v", outgoing)
	}
	buttons := outgoing.GetDocumentWithCaptionMessage().GetMessage().GetViewOnceMessage().GetMessage().GetInteractiveMessage().GetNativeFlowMessage().GetButtons()
	if len(buttons) != 1 {
		t.Fatalf("wrapped message lost its buttons: %v", outgoing)
	}
	if len(extra) != 1 || extra[0].AdditionalNodes == nil {
		t.Fatalf("extra = %#v", extra)
	}
	nodes := *extra[0].AdditionalNodes
	if len(nodes) != 2 || nodes[0].Tag != "biz" || len(nodes[0].Attrs) != 0 || nodes[1].Tag != "bot" || nodes[1].Attrs["biz_bot"] != "1" {
		t.Fatalf("direct chat nodes = %#v", nodes)
	}
	flow := nodes[0].Content.([]waBinary.Node)[0].Content.([]waBinary.Node)[0]
	if flow.Tag != "native_flow" || flow.Attrs["name"] != "mixed" || flow.Attrs["v"] != "9" {
		t.Fatalf("native_flow node = %#v", flow)
	}

	group := types.NewJID("120363000000000001", types.GroupServer)
	if _, extra := nativeFlowSend(message, group); len(*extra[0].AdditionalNodes) != 1 {
		t.Fatalf("group nodes = %#v", *extra[0].AdditionalNodes)
	}
	text := &waE2E.Message{Conversation: proto.String("hi")}
	if outgoing, extra := nativeFlowSend(text, user); outgoing != text || extra != nil {
		t.Fatal("a plain text message was changed")
	}
}
