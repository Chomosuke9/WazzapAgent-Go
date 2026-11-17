package openai

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/inbound"
)

func subtaskCall(id, arguments string) string {
	encoded, _ := json.Marshal(arguments)
	return `{"id":"` + id + `","type":"function","function":{"name":"execute_subtask","arguments":` + string(encoded) + `}}`
}

func TestExecuteSubtaskIsOfferedOnlyWhenGranted(t *testing.T) {
	providerID, _ := identity.ParseProviderID("openai-compatible")
	request := modelRequest(t, providerID)
	tools, _ := completionTools(request, inbound.CommandRegistry())
	for _, tool := range tools {
		if tool.Function.Name == "execute_subtask" {
			t.Fatal("execute_subtask offered without the capability")
		}
	}
	request.Capabilities, _ = agent.NewCapabilitySet(agent.CapabilitySubagent)
	tools, err := completionTools(request, inbound.CommandRegistry())
	if err != nil {
		t.Fatal(err)
	}
	var found *completionTool
	for index := range tools {
		if tools[index].Function.Name == "execute_subtask" {
			found = &tools[index]
		}
	}
	if found == nil || !bytes.Contains(found.Function.Parameters, []byte(`"confirmation_text"`)) || !bytes.Contains(found.Function.Parameters, []byte(`"000001"`)) {
		t.Fatalf("execute_subtask tool = %#v", found)
	}
}

func TestExecuteSubtaskDecodesFilesAndUsesTheConfirmationAsTheReply(t *testing.T) {
	providerID, _ := identity.ParseProviderID("openai-compatible")
	request := modelRequest(t, providerID)
	request.Capabilities, _ = agent.NewCapabilitySet(agent.CapabilitySubagent)
	raw := json.RawMessage(`[` + subtaskCall("call_1", `{"instruction":"Convert the PDF to DOCX","confirmation_text":"Converting your PDF now","context_msg_ids":["000001","None","000001"],"high_quality":true}`) + `]`)
	text, replyTo, effects, err := decodeModelOutput("", raw, request, inbound.CommandRegistry())
	if err != nil || text != "Converting your PDF now" || replyTo != request.CurrentMessageID || len(effects) != 1 {
		t.Fatalf("decode = %q %v %#v, err=%v", text, replyTo, effects, err)
	}
	intent := effects[0].Intent
	if intent.Kind != agent.EffectSubtask || intent.Instruction != "Convert the PDF to DOCX" || !intent.HighQuality ||
		len(intent.FileMessageIDs) != 1 || intent.FileMessageIDs[0] != request.CurrentMessageID {
		t.Fatalf("intent = %#v", intent)
	}
	if err := effects[0].Validate(request.Capabilities); err != nil {
		t.Fatal(err)
	}
}

func TestExecuteSubtaskWithAnUnknownFileIsDroppedWithItsConfirmation(t *testing.T) {
	providerID, _ := identity.ParseProviderID("openai-compatible")
	request := modelRequest(t, providerID)
	request.Capabilities, _ = agent.NewCapabilitySet(agent.CapabilitySubagent)
	reply := `{"id":"call_0","type":"function","function":{"name":"reply_message","arguments":"{\"context_msg_id\":\"none\",\"text\":\"hi\",\"choices\":null,\"command\":null,\"command_context_msg_id\":null}"}}`
	raw := json.RawMessage(`[` + reply + `,` + subtaskCall("call_1", `{"instruction":"do it","confirmation_text":"On it","context_msg_ids":["999999"],"high_quality":false}`) + `]`)
	text, _, effects, err := decodeModelOutput("", raw, request, inbound.CommandRegistry())
	if err != nil || text != "hi" || len(effects) != 0 {
		t.Fatalf("decode = %q %#v, err=%v", text, effects, err)
	}
	request.Capabilities, _ = agent.NewCapabilitySet(agent.CapabilityMessageReact)
	single := json.RawMessage(`[` + subtaskCall("call_1", `{"instruction":"do it","confirmation_text":"On it","context_msg_ids":null,"high_quality":false}`) + `]`)
	if _, _, _, err := decodeModelOutput("", single, request, inbound.CommandRegistry()); !agent.IsCode(err, agent.ErrorPermissionDenied) {
		t.Fatalf("ungranted execute_subtask err = %v", err)
	}
}
