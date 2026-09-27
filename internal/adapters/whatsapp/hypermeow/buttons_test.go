package hypermeow

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/polymorfa/hypermeow/proto/waE2E"
	"github.com/polymorfa/hypermeow/types"
	"github.com/polymorfa/hypermeow/types/events"
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

func TestQuizMessageKeepsQuoteAndLabelsChoices(t *testing.T) {
	text := &waE2E.ExtendedTextMessage{Text: proto.String("Capital?"), ContextInfo: &waE2E.ContextInfo{StanzaID: proto.String("quoted-id")}}
	interactive := quizMessage(text, []string{"Jakarta", "Bandung"}).GetViewOnceMessage().GetMessage().GetInteractiveMessage()
	buttons := interactive.GetNativeFlowMessage().GetButtons()
	if interactive.GetBody().GetText() != "Capital?" || interactive.GetContextInfo().GetStanzaID() != "quoted-id" || len(buttons) != 2 {
		t.Fatalf("quiz message = %v", interactive)
	}
	var params map[string]string
	if err := json.Unmarshal([]byte(buttons[1].GetButtonParamsJSON()), &params); err != nil || params["display_text"] != "Bandung" || params["id"] != "quiz:2" {
		t.Fatalf("quiz button = %v, %v", params, err)
	}
	// A non-command ID means a tap reads as the choice's text.
	tap := &waE2E.Message{InteractiveResponseMessage: &waE2E.InteractiveResponseMessage{
		Body: &waE2E.InteractiveResponseMessage_Body{Text: proto.String("Bandung")},
		InteractiveResponseMessage: &waE2E.InteractiveResponseMessage_NativeFlowResponseMessage_{
			NativeFlowResponseMessage: &waE2E.InteractiveResponseMessage_NativeFlowResponseMessage{ParamsJSON: proto.String(`{"id":"quiz:2"}`)},
		},
	}}
	if got, _ := buttonReply(tap); got != "Bandung" {
		t.Fatalf("quiz tap = %q", got)
	}
	if got := quizFallbackText("Capital?", []string{"Jakarta", "Bandung"}); got != "Capital?\n\n1. Jakarta\n2. Bandung\n\n_Reply with your choice._" {
		t.Fatalf("fallback = %q", got)
	}
}

func TestCopyCodeMessageCarriesTheCode(t *testing.T) {
	chat := types.NewJID("15550000002", types.DefaultUserServer)
	own := types.NewJID("15550000009", types.DefaultUserServer)
	message, err := copyCodeMessage("docker compose up -d", chat, own)
	if err != nil {
		t.Fatalf("build copy button: %v", err)
	}
	interactive := message.GetViewOnceMessage().GetMessage().GetInteractiveMessage()
	buttons := interactive.GetNativeFlowMessage().GetButtons()
	if len(buttons) != 1 || buttons[0].GetName() != "cta_copy" {
		t.Fatalf("copy message = %v", interactive)
	}
	var params map[string]string
	if err := json.Unmarshal([]byte(buttons[0].GetButtonParamsJSON()), &params); err != nil || params["copy_code"] != "docker compose up -d" {
		t.Fatalf("copy params = %v, %v", params, err)
	}
	if preview := interactive.GetContextInfo().GetQuotedMessage().GetConversation(); preview != "docker compose up -d" {
		t.Fatalf("preview = %q", preview)
	}
	if _, err := copyCodeMessage("  ", chat, own); err == nil {
		t.Fatal("empty code was accepted")
	}
}
