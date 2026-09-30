package openai

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/Chomosuke9/DiscordAgent-Go/internal/agent"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/identity"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/inbound"
)

func TestSendStickerIsOfferedOnlyWithACatalog(t *testing.T) {
	providerID, _ := identity.ParseProviderID("openai-compatible")
	request := modelRequest(t, providerID)
	request.Capabilities, _ = agent.NewCapabilitySet(agent.CapabilityMessageReact, agent.CapabilityMessageSticker)

	tools, err := completionTools(request, inbound.CommandRegistry())
	if err != nil || len(tools) != 2 {
		t.Fatalf("tools without catalog = %d, err=%v", len(tools), err)
	}

	request.Stickers = []string{"cat", "wave"}
	tools, err = completionTools(request, inbound.CommandRegistry())
	if err != nil || len(tools) != 3 {
		t.Fatalf("tools with catalog = %d, err=%v", len(tools), err)
	}
	var sticker *completionTool
	for index := range tools {
		if tools[index].Function.Name == "send_sticker" {
			sticker = &tools[index]
		}
	}
	if sticker == nil || !bytes.Contains(sticker.Function.Parameters, []byte(`"enum":["cat","wave"]`)) ||
		!bytes.Contains(sticker.Function.Parameters, []byte(`"none","000001"`)) {
		t.Fatalf("send_sticker tool = %#v", sticker)
	}
}

func TestSendStickerDecodesToStickerEffect(t *testing.T) {
	providerID, _ := identity.ParseProviderID("openai-compatible")
	request := modelRequest(t, providerID)
	request.Capabilities, _ = agent.NewCapabilitySet(agent.CapabilityMessageSticker)
	request.Stickers = []string{"wave"}

	raw := json.RawMessage(`[{"id":"call_1","type":"function","function":{"name":"send_sticker","arguments":"{\"sticker_name\":\"wave\",\"context_msg_id\":\"000001\"}"}},` +
		`{"id":"call_2","type":"function","function":{"name":"send_sticker","arguments":"{\"sticker_name\":\"wave\",\"context_msg_id\":\"none\"}"}}]`)
	text, _, effects, err := decodeModelOutput("", raw, request, inbound.CommandRegistry())
	if err != nil || text != "" || len(effects) != 2 {
		t.Fatalf("decode = %q %#v, err=%v", text, effects, err)
	}
	if effects[0].Intent.Kind != agent.EffectSticker || effects[0].Intent.Sticker != "wave" || effects[0].Intent.TargetMessageID != request.CurrentMessageID {
		t.Fatalf("quoted sticker = %#v", effects[0].Intent)
	}
	if !effects[1].Intent.TargetMessageID.IsZero() {
		t.Fatalf("unquoted sticker = %#v", effects[1].Intent)
	}
	for _, effect := range effects {
		if err := effect.Validate(request.Capabilities); err != nil {
			t.Fatalf("validate %#v: %v", effect, err)
		}
	}
}

func TestSendStickerRejectsNamesOutsideTheCatalogAndUngrantedCapability(t *testing.T) {
	providerID, _ := identity.ParseProviderID("openai-compatible")
	request := modelRequest(t, providerID)
	request.Stickers = []string{"wave"}
	unknown := json.RawMessage(`[{"id":"call_1","type":"function","function":{"name":"send_sticker","arguments":"{\"sticker_name\":\"other\",\"context_msg_id\":\"none\"}"}}]`)
	request.Capabilities, _ = agent.NewCapabilitySet(agent.CapabilityMessageSticker)
	if _, _, _, err := decodeModelOutput("", unknown, request, inbound.CommandRegistry()); err == nil {
		t.Fatal("unknown sticker name was accepted")
	}
	known := json.RawMessage(`[{"id":"call_1","type":"function","function":{"name":"send_sticker","arguments":"{\"sticker_name\":\"wave\",\"context_msg_id\":\"none\"}"}}]`)
	request.Capabilities, _ = agent.NewCapabilitySet(agent.CapabilityMessageReact)
	if _, _, _, err := decodeModelOutput("", known, request, inbound.CommandRegistry()); !agent.IsCode(err, agent.ErrorPermissionDenied) {
		t.Fatalf("ungranted sticker err = %v", err)
	}
}
