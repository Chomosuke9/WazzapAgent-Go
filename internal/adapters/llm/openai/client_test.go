package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/inbound"
)

func TestGenerateKeepsSafetyPolicyAndTypedContextSeparate(t *testing.T) {
	secret := "test-secret-never-log"
	requests := make(chan completionRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+secret {
			t.Errorf("authorization header was not configured")
		}
		var decoded completionRequest
		if err := json.NewDecoder(request.Body).Decode(&decoded); err != nil {
			t.Errorf("decode request: %v", err)
		}
		requests <- decoded
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"safe reply"}}]}`))
	}))
	defer server.Close()
	providerID, _ := identity.ParseProviderID("openai-compatible")
	client, err := New(Config{
		Endpoint: server.URL, APIKey: secret, ProviderID: providerID,
		SystemPolicy: "NON OVERRIDABLE", Timeout: time.Second, Concurrency: 1, MaxResponseBytes: 4096, Commands: inbound.CommandRegistry(),
	})
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	request := modelRequest(t, providerID)
	request.Messages = append(request.Messages[:1],
		agent.ModelMessage{Role: agent.ModelUser, Provenance: agent.ProvenancePromptOverride, Content: "chat override"},
		agent.ModelMessage{Role: agent.ModelUser, Provenance: agent.ProvenanceChatInformation, Content: "Chat information:\n- Chat state: private"},
		request.Messages[1],
	)
	result, err := client.Generate(context.Background(), request)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if result.Text != "safe reply" {
		t.Fatalf("result = %q", result.Text)
	}
	encoded := <-requests
	if encoded.Stream || encoded.MaxTokens != request.Model.MaxOutputTokens || len(encoded.Messages) != 4 {
		t.Fatalf("request envelope = %#v", encoded)
	}
	wantRoles := []string{"system", "user", "user", "user"}
	for index, role := range wantRoles {
		if encoded.Messages[index].Role != role {
			t.Fatalf("message %d role = %q, want %q", index, encoded.Messages[index].Role, role)
		}
	}
	if encoded.Messages[0].Content != "NON OVERRIDABLE\n\nbase prompt" || encoded.Messages[1].Content != "chat override" ||
		!strings.HasPrefix(encoded.Messages[2].Content, "Chat information:") || !strings.Contains(encoded.Messages[3].Content, "hello from user") {
		t.Fatalf("message ordering/content = %#v", encoded.Messages)
	}
	content := encoded.Messages[3].Content
	parts := strings.SplitN(content, "Alice 【", 2)
	if len(parts) != 2 || strings.Contains(content, "participant") {
		t.Fatalf("compact user context leaked internal identity or omitted sender ref: %q", content)
	}
	refText, _, ok := strings.Cut(parts[1], "】")
	if !ok {
		t.Fatalf("compact user context omitted sender ref terminator: %q", content)
	}
	if _, err := identity.ParseSenderRef(refText); err != nil {
		t.Fatalf("compact user context sender ref = %q: %v", refText, err)
	}
}

func TestPromptReplaceCannotReplaceSafetyPolicy(t *testing.T) {
	requestChannel := make(chan completionRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var decoded completionRequest
		_ = json.NewDecoder(request.Body).Decode(&decoded)
		requestChannel <- decoded
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"reply"}}]}`))
	}))
	defer server.Close()
	providerID, _ := identity.ParseProviderID("openai-compatible")
	client, _ := New(Config{Endpoint: server.URL, APIKey: "secret", ProviderID: providerID, SystemPolicy: "SAFETY", Timeout: time.Second, Concurrency: 1, MaxResponseBytes: 4096, Commands: inbound.CommandRegistry()})
	request := modelRequest(t, providerID)
	current := request.Messages[len(request.Messages)-1]
	request.Messages = []agent.ModelMessage{
		{Role: agent.ModelUser, Provenance: agent.ProvenancePromptOverride, Content: "replacement"},
		current,
	}
	if _, err := client.Generate(context.Background(), request); err != nil {
		t.Fatalf("generate: %v", err)
	}
	encoded := <-requestChannel
	if len(encoded.Messages) != 3 || encoded.Messages[0].Content != "SAFETY" || encoded.Messages[1].Content != "replacement" {
		t.Fatalf("replace message sequence = %#v", encoded.Messages)
	}
	for _, message := range encoded.Messages {
		if message.Content == "base prompt" {
			t.Fatal("replace mode retained configurable base prompt")
		}
	}
}

func TestProviderFailuresAreTypedAndRedacted(t *testing.T) {
	secret := "api-secret-value"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusTooManyRequests)
		_, _ = writer.Write([]byte("sensitive provider response body " + secret))
	}))
	defer server.Close()
	providerID, _ := identity.ParseProviderID("openai-compatible")
	client, _ := New(Config{Endpoint: server.URL, APIKey: secret, ProviderID: providerID, SystemPolicy: "SAFETY", Timeout: time.Second, Concurrency: 1, MaxResponseBytes: 4096, Commands: inbound.CommandRegistry()})
	_, err := client.Generate(context.Background(), modelRequest(t, providerID))
	if !agent.IsCode(err, agent.ErrorRateLimited) {
		t.Fatalf("provider error = %v, want rate_limited", err)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "sensitive provider") {
		t.Fatalf("provider error exposed secret/body: %v", err)
	}
}

func TestProviderTransportFailurePreservesNativeError(t *testing.T) {
	providerID, _ := identity.ParseProviderID("openai-compatible")
	nativeErr := fmt.Errorf("dial tcp: connection refused")
	client, err := New(Config{
		Endpoint: "https://provider.invalid/v1/chat/completions", APIKey: "secret", ProviderID: providerID,
		SystemPolicy: "SAFETY", Timeout: time.Second, Concurrency: 1, MaxResponseBytes: 4096,
		Commands: inbound.CommandRegistry(),
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, nativeErr
		})},
	})
	if err != nil {
		t.Fatalf("create client: %v", err)
	}

	_, err = client.Generate(context.Background(), modelRequest(t, providerID))
	if !agent.IsCode(err, agent.ErrorUnavailable) {
		t.Fatalf("transport error = %v, want unavailable", err)
	}
	if !errors.Is(err, nativeErr) || !strings.Contains(err.Error(), nativeErr.Error()) {
		t.Fatalf("native transport error was not preserved: %v", err)
	}
	if strings.Contains(err.Error(), "provider.invalid") {
		t.Fatalf("transport error exposed the configured provider URL: %v", err)
	}
}

func TestMalformedToolCallsAreRejected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"unsafe"}]}}]}`))
	}))
	defer server.Close()
	providerID, _ := identity.ParseProviderID("openai-compatible")
	client, _ := New(Config{Endpoint: server.URL, APIKey: "secret", ProviderID: providerID, SystemPolicy: "SAFETY", Timeout: time.Second, Concurrency: 1, MaxResponseBytes: 4096, Commands: inbound.CommandRegistry()})
	_, err := client.Generate(context.Background(), modelRequest(t, providerID))
	if !agent.IsCode(err, agent.ErrorProviderFailure) {
		t.Fatalf("tool-call error = %v, want provider_failure", err)
	}
}

func TestToolCallsDecodeToCurrentMessageBoundTypedEffects(t *testing.T) {
	requests := make(chan completionRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var decoded completionRequest
		if err := json.NewDecoder(request.Body).Decode(&decoded); err != nil {
			t.Errorf("decode tool request: %v", err)
		}
		requests <- decoded
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"noted","tool_calls":[{"id":"call_react_1","type":"function","function":{"name":"react_to_message","arguments":"{\"context_msg_id\":\"000001\",\"emoji\":\"✅\"}"}}]}}]}`))
	}))
	defer server.Close()
	providerID, _ := identity.ParseProviderID("openai-compatible")
	client, _ := New(Config{Endpoint: server.URL, APIKey: "secret", ProviderID: providerID, SystemPolicy: "SAFETY", Timeout: time.Second, Concurrency: 1, MaxResponseBytes: 4096, Commands: inbound.CommandRegistry()})
	request := modelRequest(t, providerID)
	currentMessageID, _ := identity.NewMessageID()
	capabilities, _ := agent.NewCapabilitySet("message.react")
	request.CurrentMessageID = currentMessageID
	request.ContextMessages = map[string]identity.MessageID{"000001": currentMessageID}
	request.Capabilities = capabilities
	result, err := client.Generate(context.Background(), request)
	if err != nil {
		t.Fatalf("generate tool response: %v", err)
	}
	if result.Text != "noted" || len(result.Effects) != 1 || result.Effects[0].CallID != "call_react_1" ||
		result.Effects[0].Intent.Kind != agent.EffectReact || result.Effects[0].Intent.TargetMessageID != currentMessageID || result.Effects[0].Intent.Emoji != "✅" {
		t.Fatalf("decoded typed effect = %#v", result)
	}
	encoded := <-requests
	if len(encoded.Tools) != 2 || encoded.Tools[0].Type != "function" || encoded.Tools[0].Function.Name != "reply_message" || encoded.Tools[1].Function.Name != "react_to_message" {
		t.Fatalf("tool schema = %#v", encoded.Tools)
	}
	var replySchema struct {
		Properties map[string]struct {
			Enum        []string `json:"enum"`
			Description string   `json:"description"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(encoded.Tools[0].Function.Parameters, &replySchema); err != nil {
		t.Fatalf("decode reply schema: %v", err)
	}
	if got := replySchema.Properties["context_msg_id"].Enum; len(got) != 2 || got[0] != "none" || got[1] != "000001" {
		t.Fatalf("reply context enum = %#v", got)
	}
	if !strings.Contains(encoded.Tools[0].Function.Description, "@Name (senderRef)") ||
		!strings.Contains(replySchema.Properties["text"].Description, "@Budi (a1b2c3)") ||
		!strings.Contains(replySchema.Properties["text"].Description, "@a1b2c3") ||
		!strings.Contains(replySchema.Properties["command"].Description, "No registered command") {
		t.Fatalf("reply mention guidance = %q / %q", encoded.Tools[0].Function.Description, replySchema.Properties["text"].Description)
	}
}

func TestReactionOnlyToolCallDoesNotRequireReplyText(t *testing.T) {
	providerID, _ := identity.ParseProviderID("openai-compatible")
	request := modelRequest(t, providerID)
	request.Capabilities, _ = agent.NewCapabilitySet("message.react")
	request.ContextMessages = map[string]identity.MessageID{"019277": request.CurrentMessageID}
	raw := json.RawMessage(`[{"id":"call_react_to_message_1789904909252_0","type":"function","function":{"name":"react_to_message","arguments":"{\"context_msg_id\":\"019277\",\"emoji\":\"👍\"}"}}]`)
	text, replyTo, effects, err := decodeModelOutput("", raw, request, inbound.CommandRegistry())
	if err != nil || text != "" || !replyTo.IsZero() || len(effects) != 1 ||
		effects[0].Intent.Kind != agent.EffectReact || effects[0].Intent.TargetMessageID != request.CurrentMessageID || effects[0].Intent.Emoji != "👍" {
		t.Fatalf("reaction-only decode = text %q, reply %#v, effects %#v, err %v", text, replyTo, effects, err)
	}
}

func TestToolCallCannotEscalateOrChooseArbitraryTarget(t *testing.T) {
	providerID, _ := identity.ParseProviderID("openai-compatible")
	tests := []string{
		`{"id":"call_1","type":"function","function":{"name":"reply_message","arguments":"{\"context_msg_id\":\"000001\",\"text\":\"x\",\"command\":[\"/group delete\"],\"command_context_msg_id\":[\"000001\"]}"}}`,
		`{"id":"call_2","type":"function","function":{"name":"react_to_message","arguments":"{\"context_msg_id\":\"attacker\",\"emoji\":\"✅\"}"}}`,
		`{"id":"call_3","type":"function","function":{"name":"react_to_message","arguments":"{\"context_msg_id\":\"000001\",\"emoji\":\"✅\"} {}"}}`,
	}
	for _, toolCall := range tests {
		t.Run(toolCall[:16], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"reply","tool_calls":[` + toolCall + `]}}]}`))
			}))
			defer server.Close()
			client, _ := New(Config{Endpoint: server.URL, APIKey: "secret", ProviderID: providerID, SystemPolicy: "SAFETY", Timeout: time.Second, Concurrency: 1, MaxResponseBytes: 4096, Commands: inbound.CommandRegistry()})
			request := modelRequest(t, providerID)
			request.CurrentMessageID, _ = identity.NewMessageID()
			request.ContextMessages = map[string]identity.MessageID{"000001": request.CurrentMessageID}
			request.Capabilities, _ = agent.NewCapabilitySet("message.react")
			if _, err := client.Generate(context.Background(), request); err == nil {
				t.Fatal("escalated or arbitrary-target tool call was accepted")
			}
		})
	}
}

func TestReplyMessageCarriesAuthorizedGroupCommandsWithoutStandaloneModerationTools(t *testing.T) {
	providerID, _ := identity.ParseProviderID("openai-compatible")
	request := modelRequest(t, providerID)
	target := request.ContextMessages["000001"]
	request.Capabilities, _ = agent.NewCapabilitySet("message.react", "group.delete", "group.mute", "group.kick", "command.execute")
	request.Commands = []string{"group"}

	tools, err := completionTools(request, inbound.CommandRegistry())
	if err != nil {
		t.Fatalf("build tools: %v", err)
	}
	if len(tools) != 2 || tools[0].Function.Name != "reply_message" || tools[1].Function.Name != "react_to_message" {
		t.Fatalf("provider tools = %#v", tools)
	}
	if bytes.Contains(tools[0].Function.Parameters, []byte("@Bot (bot)")) ||
		!bytes.Contains(tools[0].Function.Parameters, []byte("configured assistant name")) {
		t.Fatalf("reply tool bot mention guidance = %s", tools[0].Function.Parameters)
	}

	raw := json.RawMessage(`[{"id":"reply_1","type":"function","function":{"name":"reply_message","arguments":"{\"context_msg_id\":\"000001\",\"text\":\"done\",\"command\":[\"/group delete\",\"/group mute @Alice (abcdef) 15\",\"/group kick @Bob (123456)\"],\"command_context_msg_id\":[\"000001\",\"none\",\"none\"]}"}}]`)
	text, replyTo, effects, err := decodeModelOutput("", raw, request, inbound.CommandRegistry())
	if err != nil {
		t.Fatalf("decode reply command: %v", err)
	}
	if text != "done" || replyTo != target || len(effects) != 3 || effects[0].Intent.TargetMessageID != target ||
		effects[0].Intent.Capability() != "command.execute" || effects[1].Intent.Capability() != "command.execute" || effects[2].Intent.Capability() != "command.execute" {
		t.Fatalf("decoded reply command = %q, %#v", text, effects)
	}
}

func TestReplyMessageCarriesAuthorizedGroupDescription(t *testing.T) {
	providerID, _ := identity.ParseProviderID("openai-compatible")
	request := modelRequest(t, providerID)
	request.Capabilities, _ = agent.NewCapabilitySet("group.description", "command.execute")
	request.Commands = []string{"group"}
	tests := []struct {
		name    string
		command string
		raw     json.RawMessage
	}{
		{name: "with slash", command: "/group description Aturan baru", raw: json.RawMessage(`[{"id":"reply_description_slash","type":"function","function":{"name":"reply_message","arguments":"{\"context_msg_id\":\"none\",\"text\":\"updated\",\"command\":[\"/group description Aturan baru\"],\"command_context_msg_id\":[\"none\"]}"}}]`)},
		{name: "without slash", command: "group description Aturan baru", raw: json.RawMessage(`[{"id":"reply_description_no_slash","type":"function","function":{"name":"reply_message","arguments":"{\"context_msg_id\":\"none\",\"text\":\"updated\",\"command\":[\"group description Aturan baru\"],\"command_context_msg_id\":[\"none\"]}"}}]`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			text, replyTo, effects, err := decodeModelOutput("", test.raw, request, inbound.CommandRegistry())
			if err != nil || text != "updated" || !replyTo.IsZero() || len(effects) != 1 || effects[0].Intent.Command != "/group description Aturan baru" || effects[0].Intent.Capability() != "command.execute" {
				t.Fatalf("decoded description command %q = %q, %v, %#v", test.command, text, err, effects)
			}
		})
	}
}

func TestReplyMessageUsesAnyRegisteredCommandWithoutPerCommandSchema(t *testing.T) {
	providerID, _ := identity.ParseProviderID("openai-compatible")
	request := modelRequest(t, providerID)
	request.Capabilities, _ = agent.NewCapabilitySet("command.execute")
	request.Commands = []string{"help"}
	raw := json.RawMessage(`[{"id":"reply_help","type":"function","function":{"name":"reply_message","arguments":"{\"context_msg_id\":\"none\",\"text\":\"Baik.\",\"command\":[\"help\"],\"command_context_msg_id\":[\"none\"]}"}}]`)

	text, _, effects, err := decodeModelOutput("", raw, request, inbound.CommandRegistry())
	if err != nil || text != "Baik." || len(effects) != 1 || effects[0].Intent.Command != "/help" {
		t.Fatalf("registered command = %q, %#v, %v", text, effects, err)
	}
}

func TestReplyMessageSkipsMalformedGroupCommandAndKeepsReply(t *testing.T) {
	providerID, _ := identity.ParseProviderID("openai-compatible")
	request := modelRequest(t, providerID)
	request.Capabilities, _ = agent.NewCapabilitySet("group.description", "command.execute")
	request.Commands = []string{"group"}
	raw := json.RawMessage(`[{"id":"reply_invalid_command","type":"function","function":{"name":"reply_message","arguments":"{\"context_msg_id\":\"none\",\"text\":\"trying\",\"command\":[\"none\",\"/group description\",\"/group description Aturan baru\"],\"command_context_msg_id\":[\"none\",\"none\",\"none\"]}"}}]`)
	text, replyTo, effects, err := decodeModelOutput("", raw, request, inbound.CommandRegistry())
	if err != nil || text != "trying" || !replyTo.IsZero() {
		t.Fatalf("reply with malformed commands = %q, %v, %v", text, replyTo, err)
	}
	if len(effects) != 2 || effects[0].Intent.Command != "/group description" || effects[1].CallID != "reply_invalid_command:2" || effects[1].Intent.Command != "/group description Aturan baru" {
		t.Fatalf("registered effects after unknown command = %#v", effects)
	}
}

func TestReplyMessageDefaultsDeleteAnchorAndIgnoresUnknownPlainReplyContext(t *testing.T) {
	providerID, _ := identity.ParseProviderID("openai-compatible")
	request := modelRequest(t, providerID)
	request.Capabilities, _ = agent.NewCapabilitySet("message.react", "group.delete", "command.execute")
	request.Commands = []string{"group"}
	raw := json.RawMessage(`[{"id":"reply_1","type":"function","function":{"name":"reply_message","arguments":"{\"context_msg_id\":\"000001\",\"text\":\"deleted\",\"command\":[\"/group delete\"],\"command_context_msg_id\":null}"}}]`)
	_, replyTo, effects, err := decodeModelOutput("", raw, request, inbound.CommandRegistry())
	if err != nil || replyTo != request.ContextMessages["000001"] || len(effects) != 1 || effects[0].Intent.TargetMessageID != request.ContextMessages["000001"] {
		t.Fatalf("default command anchor = %#v, %v", effects, err)
	}

	plain := json.RawMessage(`[{"id":"reply_2","type":"function","function":{"name":"reply_message","arguments":"{\"context_msg_id\":\"999999\",\"text\":\"x\",\"command\":null,\"command_context_msg_id\":null}"}}]`)
	text, replyTo, effects, err := decodeModelOutput("", plain, request, inbound.CommandRegistry())
	if err != nil || text != "x" || !replyTo.IsZero() || len(effects) != 0 {
		t.Fatalf("unknown plain reply context = %q, %#v, %v", text, effects, err)
	}

	delete := json.RawMessage(`[{"id":"reply_3","type":"function","function":{"name":"reply_message","arguments":"{\"context_msg_id\":\"999999\",\"text\":\"x\",\"command\":[\"/group delete\"],\"command_context_msg_id\":null}"}}]`)
	if _, _, _, err := decodeModelOutput("", delete, request, inbound.CommandRegistry()); !agent.IsCode(err, agent.ErrorProviderFailure) {
		t.Fatalf("unknown delete anchor error = %v", err)
	}
}

func TestReplyMessageAcceptsEmptyAndUnevenCommandContextArrays(t *testing.T) {
	providerID, _ := identity.ParseProviderID("openai-compatible")
	request := modelRequest(t, providerID)
	request.Capabilities, _ = agent.NewCapabilitySet("message.react", "group.delete", "command.execute")
	request.Commands = []string{"group"}

	empty := json.RawMessage(`[{"id":"reply_empty","type":"function","function":{"name":"reply_message","arguments":"{\"context_msg_id\":\"000001\",\"text\":\"plain reply\",\"command\":[],\"command_context_msg_id\":[]}"}}]`)
	text, replyTo, effects, err := decodeModelOutput("", empty, request, inbound.CommandRegistry())
	if err != nil || text != "plain reply" || replyTo != request.ContextMessages["000001"] || len(effects) != 0 {
		t.Fatalf("empty command arrays = %q, %#v, %v", text, effects, err)
	}

	shortContexts := json.RawMessage(`[{"id":"reply_short","type":"function","function":{"name":"reply_message","arguments":"{\"context_msg_id\":\"000001\",\"text\":\"deleted\",\"command\":[\"/group delete\"],\"command_context_msg_id\":[]}"}}]`)
	_, replyTo, effects, err = decodeModelOutput("", shortContexts, request, inbound.CommandRegistry())
	if err != nil || replyTo != request.ContextMessages["000001"] || len(effects) != 1 || effects[0].Intent.TargetMessageID != request.ContextMessages["000001"] {
		t.Fatalf("short command contexts = %#v, %v", effects, err)
	}
}

func TestReplyMessageCarriesSelectedQuoteWithNoCommands(t *testing.T) {
	providerID, _ := identity.ParseProviderID("openai-compatible")
	request := modelRequest(t, providerID)
	target, _ := identity.NewMessageID()
	request.ContextMessages["000354"] = target
	raw := json.RawMessage(`[{"id":"reply_354","type":"function","function":{"name":"reply_message","arguments":"{\"command_context_msg_id\":[\"none\"],\"command\":[],\"context_msg_id\":\"000354\",\"text\":\"Halo! Ini balasan untuk pesan terbaru kamu.\"}"}}]`)
	text, replyTo, effects, err := decodeModelOutput("", raw, request, inbound.CommandRegistry())
	if err != nil || text != "Halo! Ini balasan untuk pesan terbaru kamu." || replyTo != target || len(effects) != 0 {
		t.Fatalf("selected reply target = %q, %v, %#v, %v", text, replyTo, effects, err)
	}
}

func TestConfiguredResponseLimitIsEnforced(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"123456"}}]}`))
	}))
	defer server.Close()
	providerID, _ := identity.ParseProviderID("openai-compatible")
	client, err := New(Config{
		Endpoint: server.URL, APIKey: "secret", ProviderID: providerID, SystemPolicy: "SAFETY",
		Timeout: time.Second, Concurrency: 1, MaxResponseBytes: 5, Commands: inbound.CommandRegistry(),
	})
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	_, err = client.Generate(context.Background(), modelRequest(t, providerID))
	if !agent.IsCode(err, agent.ErrorProviderFailure) {
		t.Fatalf("oversized response error = %v, want provider_failure", err)
	}
}

func TestClientTimeoutAppliesToCustomHTTPClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"late"}}]}`))
	}))
	defer server.Close()
	providerID, _ := identity.ParseProviderID("openai-compatible")
	client, err := New(Config{
		Endpoint: server.URL, APIKey: "secret", ProviderID: providerID, SystemPolicy: "SAFETY",
		Timeout: 20 * time.Millisecond, Concurrency: 1, MaxResponseBytes: 4096, HTTPClient: &http.Client{}, Commands: inbound.CommandRegistry(),
	})
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	_, err = client.Generate(context.Background(), modelRequest(t, providerID))
	if !agent.IsCode(err, agent.ErrorTimeout) {
		t.Fatalf("timeout error = %v, want timeout", err)
	}
}

func TestProviderBodyLimitAndMalformedJSONAreRejected(t *testing.T) {
	providerID, _ := identity.ParseProviderID("openai-compatible")
	tests := []struct {
		name string
		body string
	}{
		{name: "malformed", body: `{"choices":`},
		{name: "oversized body", body: strings.Repeat("x", maxProviderBodyBytes+1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write([]byte(test.body))
			}))
			defer server.Close()
			client, err := New(Config{
				Endpoint: server.URL, APIKey: "secret", ProviderID: providerID, SystemPolicy: "SAFETY",
				Timeout: time.Second, Concurrency: 1, MaxResponseBytes: 4096, Commands: inbound.CommandRegistry(),
			})
			if err != nil {
				t.Fatalf("create client: %v", err)
			}
			_, err = client.Generate(context.Background(), modelRequest(t, providerID))
			if !agent.IsCode(err, agent.ErrorProviderFailure) {
				t.Fatalf("provider response error = %v, want provider_failure", err)
			}
		})
	}
}

func TestGlobalModelConcurrencyIsBounded(t *testing.T) {
	providerID, _ := identity.ParseProviderID("openai-compatible")
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var calls atomic.Int32
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		started <- struct{}{}
		select {
		case <-request.Context().Done():
			return nil, request.Context().Err()
		case <-release:
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewBufferString(`{"choices":[{"message":{"role":"assistant","content":"reply"}}]}`)),
				Header:     make(http.Header),
			}, nil
		}
	})
	client, err := New(Config{
		Endpoint: "https://llm.example.invalid/v1/chat/completions", APIKey: "secret", ProviderID: providerID,
		SystemPolicy: "SAFETY", Timeout: time.Second, Concurrency: 1, MaxResponseBytes: 4096,
		HTTPClient: &http.Client{Transport: transport},
		Commands:   inbound.CommandRegistry(),
	})
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	first := make(chan error, 1)
	go func() {
		_, err := client.Generate(context.Background(), modelRequest(t, providerID))
		first <- err
	}()
	<-started
	secondCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, secondErr := client.Generate(secondCtx, modelRequest(t, providerID))
	if !agent.IsCode(secondErr, agent.ErrorTimeout) {
		t.Fatalf("capacity wait error = %v, want timeout", secondErr)
	}
	if calls.Load() != 1 {
		t.Fatalf("HTTP calls while capacity full = %d, want 1", calls.Load())
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatalf("first model call: %v", err)
	}
}

func TestRedirectIsNotFollowedWithAuthorizationHeader(t *testing.T) {
	var redirectedCalls atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirectedCalls.Add(1)
	}))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	providerID, _ := identity.ParseProviderID("openai-compatible")
	client, err := New(Config{
		Endpoint: source.URL, APIKey: "secret", ProviderID: providerID, SystemPolicy: "SAFETY",
		Timeout: time.Second, Concurrency: 1, MaxResponseBytes: 4096, Commands: inbound.CommandRegistry(),
	})
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	_, err = client.Generate(context.Background(), modelRequest(t, providerID))
	if !agent.IsCode(err, agent.ErrorProviderFailure) {
		t.Fatalf("redirect error = %v, want provider_failure", err)
	}
	if redirectedCalls.Load() != 0 {
		t.Fatal("LLM client followed a redirect with credentials")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func modelRequest(t *testing.T, providerID identity.ProviderID) agent.ModelRequest {
	t.Helper()
	tenantID, _ := identity.NewTenantID()
	accountID, _ := identity.NewAccountID()
	chatID, _ := identity.NewChatID()
	invocationID, _ := identity.NewInvocationID()
	senderRef, _ := identity.NewSenderRef()
	currentMessageID, _ := identity.NewMessageID()
	capabilities, _ := agent.NewCapabilitySet("message.react")
	return agent.ModelRequest{
		Key:           agent.Key{TenantID: tenantID, AccountID: accountID, ChatID: chatID},
		InvocationID:  invocationID,
		ConfigVersion: 1,
		Model:         agent.ModelConfig{ProviderID: providerID, Model: "test-model", MaxOutputTokens: 128},
		Messages: []agent.ModelMessage{
			{Role: agent.ModelSystem, Provenance: agent.ProvenanceBasePrompt, Content: "base prompt"},
			{Role: agent.ModelUser, Provenance: agent.ProvenanceCurrentUser,
				Content: "【000001】 00:00\nAlice 【" + senderRef.String() + "】: hello from user"},
		},
		Capabilities:     capabilities,
		CurrentMessageID: currentMessageID,
		ContextMessages:  map[string]identity.MessageID{"000001": currentMessageID},
	}
}
