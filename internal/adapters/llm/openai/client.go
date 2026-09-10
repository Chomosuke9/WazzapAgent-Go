package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

const maxProviderBodyBytes = 1 << 20

type Config struct {
	Endpoint         string
	APIKey           string
	ProviderID       identity.ProviderID
	SystemPolicy     string
	Timeout          time.Duration
	Concurrency      uint32
	MaxResponseBytes uint32
	HTTPClient       *http.Client
	Observer         Observer
}

type Observer interface {
	ObserveModel(time.Duration, agent.ErrorCode)
}

type discardObserver struct{}

func (discardObserver) ObserveModel(time.Duration, agent.ErrorCode) {}

type Client struct {
	endpoint         string
	apiKey           string
	providerID       identity.ProviderID
	systemPolicy     string
	httpClient       *http.Client
	semaphore        chan struct{}
	timeout          time.Duration
	maxResponseBytes uint32
	observer         Observer
}

func New(config Config) (*Client, error) {
	endpoint, err := validateEndpoint(config.Endpoint)
	if err != nil {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "create LLM client", err)
	}
	if strings.TrimSpace(config.APIKey) == "" || config.ProviderID.IsZero() || strings.TrimSpace(config.SystemPolicy) == "" {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "create LLM client", fmt.Errorf("credentials, provider ID, and system policy are required"))
	}
	if config.Timeout <= 0 || config.Timeout > 10*time.Minute {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "create LLM client", fmt.Errorf("timeout must be positive and at most 10 minutes"))
	}
	if config.Concurrency == 0 || config.Concurrency > 256 {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "create LLM client", fmt.Errorf("concurrency must be between 1 and 256"))
	}
	if config.MaxResponseBytes == 0 || config.MaxResponseBytes > agent.MaxResponseBytes {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "create LLM client", fmt.Errorf("response limit must be between 1 and %d bytes", agent.MaxResponseBytes))
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: config.Timeout}
	}
	clientCopy := *httpClient
	if clientCopy.CheckRedirect == nil {
		clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}
	}
	observer := config.Observer
	if observer == nil {
		observer = discardObserver{}
	}
	return &Client{
		endpoint:         endpoint,
		apiKey:           config.APIKey,
		providerID:       config.ProviderID,
		systemPolicy:     config.SystemPolicy,
		httpClient:       &clientCopy,
		semaphore:        make(chan struct{}, config.Concurrency),
		timeout:          config.Timeout,
		maxResponseBytes: config.MaxResponseBytes,
		observer:         observer,
	}, nil
}

func (client *Client) Generate(ctx context.Context, request agent.ModelRequest) (result agent.ModelResult, resultErr error) {
	started := time.Now()
	defer func() { client.observer.ObserveModel(time.Since(started), agent.CodeOf(resultErr)) }()
	if request.Model.ProviderID != client.providerID {
		return agent.ModelResult{}, agent.NewError(agent.ErrorUnsupported, "generate model response", fmt.Errorf("provider is not configured"))
	}
	requestCtx, cancel := context.WithTimeout(ctx, client.timeout)
	defer cancel()
	select {
	case <-requestCtx.Done():
		return agent.ModelResult{}, llmContextError("wait for model capacity", requestCtx.Err())
	case client.semaphore <- struct{}{}:
	}
	defer func() { <-client.semaphore }()

	messages, tools, err := client.messages(request)
	if err != nil {
		return agent.ModelResult{}, err
	}
	payload, err := json.Marshal(completionRequest{
		Model:     request.Model.Model,
		Messages:  messages,
		MaxTokens: request.Model.MaxOutputTokens,
		Stream:    false,
		Tools:     tools,
	})
	if err != nil {
		return agent.ModelResult{}, agent.NewError(agent.ErrorInternal, "encode model request", err)
	}
	httpRequest, err := http.NewRequestWithContext(requestCtx, http.MethodPost, client.endpoint, bytes.NewReader(payload))
	if err != nil {
		return agent.ModelResult{}, agent.NewError(agent.ErrorInternal, "create model request", err)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+client.apiKey)
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")
	httpResponse, err := client.httpClient.Do(httpRequest)
	if err != nil {
		if requestCtx.Err() != nil {
			return agent.ModelResult{}, llmContextError("invoke model", requestCtx.Err())
		}
		return agent.ModelResult{}, agent.NewError(agent.ErrorUnavailable, "invoke model", fmt.Errorf("provider request failed"))
	}
	defer httpResponse.Body.Close()
	if httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(httpResponse.Body, maxProviderBodyBytes))
		return agent.ModelResult{}, statusError(httpResponse.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(httpResponse.Body, maxProviderBodyBytes+1))
	if err != nil {
		return agent.ModelResult{}, agent.NewError(agent.ErrorProviderFailure, "read model response", fmt.Errorf("provider response could not be read"))
	}
	if len(body) > maxProviderBodyBytes {
		return agent.ModelResult{}, agent.NewError(agent.ErrorProviderFailure, "read model response", fmt.Errorf("provider response body is too large"))
	}
	var decoded completionResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return agent.ModelResult{}, agent.NewError(agent.ErrorProviderFailure, "decode model response", fmt.Errorf("provider returned invalid JSON"))
	}
	if len(decoded.Choices) == 0 {
		return agent.ModelResult{}, agent.NewError(agent.ErrorProviderFailure, "decode model response", fmt.Errorf("provider returned no choices"))
	}
	message := decoded.Choices[0].Message
	if message.Role != "" && message.Role != "assistant" {
		return agent.ModelResult{}, agent.NewError(agent.ErrorProviderFailure, "decode model response", fmt.Errorf("unexpected response role"))
	}
	if len(message.Content) > int(client.maxResponseBytes) {
		return agent.ModelResult{}, agent.NewError(agent.ErrorProviderFailure, "decode model response", fmt.Errorf("provider response exceeds configured byte limit"))
	}
	effects, err := decodeToolEffects(message.ToolCalls, request)
	if err != nil {
		return agent.ModelResult{}, err
	}
	return agent.ModelResult{Text: message.Content, Effects: effects}, nil
}

func (client *Client) messages(request agent.ModelRequest) ([]completionMessage, []completionTool, error) {
	if strings.TrimSpace(request.Model.Model) == "" || request.Model.MaxOutputTokens == 0 {
		return nil, nil, agent.NewError(agent.ErrorInvalidArgument, "build model request", fmt.Errorf("model configuration is required"))
	}
	if err := agent.ValidateModelMessages(request.Messages); err != nil {
		return nil, nil, err
	}
	tools, err := completionTools(request)
	if err != nil {
		return nil, nil, err
	}
	messages := []completionMessage{{Role: "system", Content: client.systemPolicy}}
	for _, message := range request.Messages {
		role := ""
		switch message.Role {
		case agent.ModelSystem:
			role = "system"
		case agent.ModelUser:
			role = "user"
		case agent.ModelAssistant:
			role = "assistant"
		default:
			return nil, nil, agent.NewError(agent.ErrorInvalidArgument, "build model request", fmt.Errorf("invalid model message role"))
		}
		messages = append(messages, completionMessage{Role: role, Content: message.Content})
	}
	return messages, tools, nil
}

type completionRequest struct {
	Model     string              `json:"model"`
	Messages  []completionMessage `json:"messages"`
	MaxTokens uint32              `json:"max_tokens"`
	Stream    bool                `json:"stream"`
	Tools     []completionTool    `json:"tools,omitempty"`
}

type completionMessage struct {
	Role      string          `json:"role"`
	Content   string          `json:"content"`
	ToolCalls json.RawMessage `json:"tool_calls,omitempty"`
}

type completionResponse struct {
	Choices []struct {
		Message completionMessage `json:"message"`
	} `json:"choices"`
}

type completionTool struct {
	Type     string             `json:"type"`
	Function completionFunction `json:"function"`
}

type completionFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Arguments   json.RawMessage `json:"arguments,omitempty"`
}

type completionToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function completionFunction `json:"function"`
}

func completionTools(request agent.ModelRequest) ([]completionTool, error) {
	capabilities := request.Capabilities.Values()
	if len(capabilities) == 0 {
		return nil, nil
	}
	if request.CurrentMessageID.IsZero() {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "build model tools", fmt.Errorf("current message identity is required for tools"))
	}
	tools := make([]completionTool, 0, len(capabilities))
	for _, capability := range capabilities {
		name, description, parameters, ok := toolSchema(capability)
		if !ok {
			return nil, agent.NewError(agent.ErrorUnsupported, "build model tools", fmt.Errorf("capability is not exposed to this provider"))
		}
		tools = append(tools, completionTool{Type: "function", Function: completionFunction{Name: name, Description: description, Parameters: parameters}})
	}
	return tools, nil
}

func toolSchema(capability agent.Capability) (string, string, json.RawMessage, bool) {
	const emptyObject = `{"type":"object","properties":{},"additionalProperties":false}`
	switch capability {
	case "message.react":
		return "wazzap_react", "React to the current inbound message.", json.RawMessage(`{"type":"object","properties":{"emoji":{"type":"string","maxLength":64}},"required":["emoji"],"additionalProperties":false}`), true
	case "message.delete":
		return "wazzap_delete_current", "Delete the current inbound message when policy permits.", json.RawMessage(emptyObject), true
	case "message.mark-read":
		return "wazzap_mark_read", "Mark the current inbound message as read.", json.RawMessage(emptyObject), true
	case "chat.presence":
		return "wazzap_set_presence", "Set composing or paused presence for this chat.", json.RawMessage(`{"type":"object","properties":{"state":{"type":"string","enum":["composing","paused"]}},"required":["state"],"additionalProperties":false}`), true
	default:
		return "", "", nil, false
	}
}

func decodeToolEffects(raw json.RawMessage, request agent.ModelRequest) ([]agent.ModelEffect, error) {
	if len(raw) == 0 || string(raw) == "null" || string(raw) == "[]" {
		return nil, nil
	}
	if len(request.Capabilities.Values()) == 0 {
		return nil, agent.NewError(agent.ErrorUnsupported, "decode model response", fmt.Errorf("model returned tools without granted capabilities"))
	}
	if request.CurrentMessageID.IsZero() {
		return nil, agent.NewError(agent.ErrorIntegrityFailure, "decode model response", fmt.Errorf("tool response has no current message target"))
	}
	var calls []completionToolCall
	if err := json.Unmarshal(raw, &calls); err != nil || len(calls) == 0 || len(calls) > agent.MaxModelEffects {
		return nil, agent.NewError(agent.ErrorProviderFailure, "decode model response", fmt.Errorf("tool calls are invalid or exceed the limit"))
	}
	allowed := make(map[agent.Capability]struct{}, len(request.Capabilities.Values()))
	for _, capability := range request.Capabilities.Values() {
		allowed[capability] = struct{}{}
	}
	seen := make(map[string]struct{}, len(calls))
	effects := make([]agent.ModelEffect, 0, len(calls))
	for _, call := range calls {
		if call.Type != "function" || strings.TrimSpace(call.ID) != call.ID || call.ID == "" || len(call.ID) > 128 {
			return nil, agent.NewError(agent.ErrorProviderFailure, "decode model response", fmt.Errorf("tool call identity is invalid"))
		}
		if _, exists := seen[call.ID]; exists {
			return nil, agent.NewError(agent.ErrorProviderFailure, "decode model response", fmt.Errorf("tool call ID is duplicated"))
		}
		seen[call.ID] = struct{}{}
		intent, capability, err := decodeToolIntent(call.Function, request.CurrentMessageID)
		if err != nil {
			return nil, err
		}
		if _, exists := allowed[capability]; !exists {
			return nil, agent.NewError(agent.ErrorPermissionDenied, "decode model response", fmt.Errorf("tool capability was not granted"))
		}
		effects = append(effects, agent.ModelEffect{CallID: call.ID, Intent: intent})
	}
	return effects, nil
}

func decodeToolIntent(function completionFunction, currentMessageID identity.MessageID) (agent.EffectIntent, agent.Capability, error) {
	if len(function.Arguments) > 4*1024 {
		return agent.EffectIntent{}, "", agent.NewError(agent.ErrorProviderFailure, "decode model response", fmt.Errorf("tool arguments exceed the limit"))
	}
	decode := func(value any) error {
		var arguments string
		if err := json.Unmarshal(function.Arguments, &arguments); err != nil || len(arguments) > 4*1024 {
			return agent.NewError(agent.ErrorProviderFailure, "decode model response", fmt.Errorf("tool arguments are invalid"))
		}
		decoder := json.NewDecoder(strings.NewReader(arguments))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(value); err != nil {
			return agent.NewError(agent.ErrorProviderFailure, "decode model response", fmt.Errorf("tool arguments are invalid"))
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			return agent.NewError(agent.ErrorProviderFailure, "decode model response", fmt.Errorf("tool arguments contain extra values"))
		}
		return nil
	}
	switch function.Name {
	case "wazzap_react":
		var args struct {
			Emoji string `json:"emoji"`
		}
		if err := decode(&args); err != nil {
			return agent.EffectIntent{}, "", err
		}
		return agent.EffectIntent{Kind: agent.EffectReact, TargetMessageID: currentMessageID, Emoji: args.Emoji}, "message.react", nil
	case "wazzap_delete_current":
		var args struct{}
		if err := decode(&args); err != nil {
			return agent.EffectIntent{}, "", err
		}
		return agent.EffectIntent{Kind: agent.EffectDeleteMessage, TargetMessageID: currentMessageID}, "message.delete", nil
	case "wazzap_mark_read":
		var args struct{}
		if err := decode(&args); err != nil {
			return agent.EffectIntent{}, "", err
		}
		return agent.EffectIntent{Kind: agent.EffectMarkRead, TargetMessageID: currentMessageID}, "message.mark-read", nil
	case "wazzap_set_presence":
		var args struct {
			State agent.PresenceState `json:"state"`
		}
		if err := decode(&args); err != nil {
			return agent.EffectIntent{}, "", err
		}
		return agent.EffectIntent{Kind: agent.EffectSetChatPresence, Presence: args.State}, "chat.presence", nil
	default:
		return agent.EffectIntent{}, "", agent.NewError(agent.ErrorProviderFailure, "decode model response", fmt.Errorf("tool name is not declared"))
	}
}

func validateEndpoint(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("LLM endpoint must be an absolute URL")
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return "", fmt.Errorf("LLM endpoint must use HTTP or HTTPS")
	}
	if parsed.User != nil || parsed.Fragment != "" {
		return "", fmt.Errorf("LLM endpoint must not contain credentials or a fragment")
	}
	return parsed.String(), nil
}

func statusError(status int) error {
	switch {
	case status == http.StatusRequestTimeout || status == http.StatusGatewayTimeout:
		return agent.NewError(agent.ErrorTimeout, "invoke model", fmt.Errorf("provider returned HTTP %d", status))
	case status == http.StatusTooManyRequests:
		return agent.NewError(agent.ErrorRateLimited, "invoke model", fmt.Errorf("provider returned HTTP %d", status))
	case status >= 500:
		return agent.NewError(agent.ErrorUnavailable, "invoke model", fmt.Errorf("provider returned HTTP %d", status))
	default:
		return agent.NewError(agent.ErrorProviderFailure, "invoke model", fmt.Errorf("provider returned HTTP %d", status))
	}
}

func llmContextError(operation string, err error) error {
	if err == context.DeadlineExceeded {
		return agent.NewError(agent.ErrorTimeout, operation, err)
	}
	return agent.NewError(agent.ErrorCancelled, operation, err)
}
