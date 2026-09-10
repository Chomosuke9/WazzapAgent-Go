package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
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
		SystemPolicy: "NON OVERRIDABLE", Timeout: time.Second, Concurrency: 1, MaxResponseBytes: 4096,
	})
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	request := modelRequest(t, providerID)
	request.Messages = append(request.Messages[:1],
		agent.ModelMessage{Role: agent.ModelSystem, Provenance: agent.ProvenancePromptOverride, Content: "chat override"},
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
	wantRoles := []string{"system", "system", "system", "user"}
	for index, role := range wantRoles {
		if encoded.Messages[index].Role != role {
			t.Fatalf("message %d role = %q, want %q", index, encoded.Messages[index].Role, role)
		}
	}
	if encoded.Messages[0].Content != "NON OVERRIDABLE" || encoded.Messages[1].Content != "base prompt" ||
		encoded.Messages[2].Content != "chat override" || !strings.Contains(encoded.Messages[3].Content, "hello from user") {
		t.Fatalf("message ordering/content = %#v", encoded.Messages)
	}
	if !strings.Contains(encoded.Messages[3].Content, "【u_") || strings.Contains(encoded.Messages[3].Content, "participant") {
		t.Fatalf("compact user context leaked internal identity or omitted sender ref: %q", encoded.Messages[3].Content)
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
	client, _ := New(Config{Endpoint: server.URL, APIKey: "secret", ProviderID: providerID, SystemPolicy: "SAFETY", Timeout: time.Second, Concurrency: 1, MaxResponseBytes: 4096})
	request := modelRequest(t, providerID)
	current := request.Messages[len(request.Messages)-1]
	request.Messages = []agent.ModelMessage{
		{Role: agent.ModelSystem, Provenance: agent.ProvenancePromptOverride, Content: "replacement"},
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
	client, _ := New(Config{Endpoint: server.URL, APIKey: secret, ProviderID: providerID, SystemPolicy: "SAFETY", Timeout: time.Second, Concurrency: 1, MaxResponseBytes: 4096})
	_, err := client.Generate(context.Background(), modelRequest(t, providerID))
	if !agent.IsCode(err, agent.ErrorRateLimited) {
		t.Fatalf("provider error = %v, want rate_limited", err)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "sensitive provider") {
		t.Fatalf("provider error exposed secret/body: %v", err)
	}
}

func TestToolCallsAreRejectedInPartOne(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"unsafe"}]}}]}`))
	}))
	defer server.Close()
	providerID, _ := identity.ParseProviderID("openai-compatible")
	client, _ := New(Config{Endpoint: server.URL, APIKey: "secret", ProviderID: providerID, SystemPolicy: "SAFETY", Timeout: time.Second, Concurrency: 1, MaxResponseBytes: 4096})
	_, err := client.Generate(context.Background(), modelRequest(t, providerID))
	if !agent.IsCode(err, agent.ErrorUnsupported) {
		t.Fatalf("tool-call error = %v, want unsupported", err)
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
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"noted","tool_calls":[{"id":"call_react_1","type":"function","function":{"name":"wazzap_react","arguments":"{\"emoji\":\"✅\"}"}}]}}]}`))
	}))
	defer server.Close()
	providerID, _ := identity.ParseProviderID("openai-compatible")
	client, _ := New(Config{Endpoint: server.URL, APIKey: "secret", ProviderID: providerID, SystemPolicy: "SAFETY", Timeout: time.Second, Concurrency: 1, MaxResponseBytes: 4096})
	request := modelRequest(t, providerID)
	currentMessageID, _ := identity.NewMessageID()
	capabilities, _ := agent.NewCapabilitySet("message.react")
	request.CurrentMessageID = currentMessageID
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
	if len(encoded.Tools) != 1 || encoded.Tools[0].Type != "function" || encoded.Tools[0].Function.Name != "wazzap_react" {
		t.Fatalf("tool schema = %#v", encoded.Tools)
	}
}

func TestToolCallCannotEscalateOrChooseArbitraryTarget(t *testing.T) {
	providerID, _ := identity.ParseProviderID("openai-compatible")
	tests := []string{
		`{"id":"call_1","type":"function","function":{"name":"wazzap_delete_current","arguments":"{}"}}`,
		`{"id":"call_2","type":"function","function":{"name":"wazzap_react","arguments":"{\"emoji\":\"✅\",\"target_message_id\":\"attacker\"}"}}`,
		`{"id":"call_3","type":"function","function":{"name":"wazzap_react","arguments":"{\"emoji\":\"✅\"} {}"}}`,
	}
	for _, toolCall := range tests {
		t.Run(toolCall[:16], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"reply","tool_calls":[` + toolCall + `]}}]}`))
			}))
			defer server.Close()
			client, _ := New(Config{Endpoint: server.URL, APIKey: "secret", ProviderID: providerID, SystemPolicy: "SAFETY", Timeout: time.Second, Concurrency: 1, MaxResponseBytes: 4096})
			request := modelRequest(t, providerID)
			request.CurrentMessageID, _ = identity.NewMessageID()
			request.Capabilities, _ = agent.NewCapabilitySet("message.react")
			if _, err := client.Generate(context.Background(), request); err == nil {
				t.Fatal("escalated or arbitrary-target tool call was accepted")
			}
		})
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
		Timeout: time.Second, Concurrency: 1, MaxResponseBytes: 5,
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
		Timeout: 20 * time.Millisecond, Concurrency: 1, MaxResponseBytes: 4096, HTTPClient: &http.Client{},
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
				Timeout: time.Second, Concurrency: 1, MaxResponseBytes: 4096,
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
		Timeout: time.Second, Concurrency: 1, MaxResponseBytes: 4096,
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
	capabilities, _ := agent.NewCapabilitySet()
	return agent.ModelRequest{
		Key:           agent.Key{TenantID: tenantID, AccountID: accountID, ChatID: chatID},
		InvocationID:  invocationID,
		ConfigVersion: 1,
		Model:         agent.ModelConfig{ProviderID: providerID, Model: "test-model", MaxOutputTokens: 128},
		Messages: []agent.ModelMessage{
			{Role: agent.ModelSystem, Provenance: agent.ProvenanceBasePrompt, Content: "base prompt"},
			{Role: agent.ModelUser, Provenance: agent.ProvenanceCurrentUser,
				Content: "【#000001】 00:00\nAlice 【" + senderRef.String() + "】: hello from user"},
		},
		Capabilities: capabilities,
	}
}
