package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
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
	Commands         *command.Registry
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
	commands         *command.Registry
}

func New(config Config) (*Client, error) {
	endpoint, err := validateEndpoint(config.Endpoint)
	if err != nil {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "create LLM client", err)
	}
	if strings.TrimSpace(config.APIKey) == "" || config.ProviderID.IsZero() || strings.TrimSpace(config.SystemPolicy) == "" || config.Commands == nil {
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
		commands:         config.Commands,
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
		return agent.ModelResult{}, agent.NewError(agent.ErrorUnavailable, "invoke model", providerRequestError(err))
	}
	defer httpResponse.Body.Close()
	if httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(httpResponse.Body, maxProviderBodyBytes))
		return agent.ModelResult{}, statusError(httpResponse.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(httpResponse.Body, maxProviderBodyBytes+1))
	if err != nil {
		return agent.ModelResult{}, agent.NewError(agent.ErrorProviderFailure, "read model response", err)
	}
	if len(body) > maxProviderBodyBytes {
		return agent.ModelResult{}, agent.NewError(agent.ErrorProviderFailure, "read model response", fmt.Errorf("provider response body is too large"))
	}
	var decoded completionResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return agent.ModelResult{}, agent.NewError(agent.ErrorProviderFailure, "decode model response", err)
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
	text, replyTo, effects, err := decodeModelOutput(message.Content, message.ToolCalls, request, client.commands)
	if err != nil {
		return agent.ModelResult{}, err
	}
	return agent.ModelResult{Text: text, ReplyToMessageID: replyTo, Effects: effects}, nil
}

func (client *Client) messages(request agent.ModelRequest) ([]completionMessage, []completionTool, error) {
	if strings.TrimSpace(request.Model.Model) == "" || request.Model.MaxOutputTokens == 0 {
		return nil, nil, agent.NewError(agent.ErrorInvalidArgument, "build model request", fmt.Errorf("model configuration is required"))
	}
	if err := agent.ValidateModelMessages(request.Messages); err != nil {
		return nil, nil, err
	}
	tools, err := completionTools(request, client.commands)
	if err != nil {
		return nil, nil, err
	}
	systemContent := client.systemPolicy
	additionalPromptCount := strings.Count(systemContent, "{{additional_prompt}}")
	if additionalPromptCount > 1 {
		return nil, nil, agent.NewError(agent.ErrorIntegrityFailure, "build model request", fmt.Errorf("system policy has duplicate additional prompt placeholders"))
	}
	var basePrompt, additionalPrompt string
	for _, message := range request.Messages {
		if message.Provenance == agent.ProvenanceBasePrompt {
			basePrompt = message.Content
			additionalPrompt = message.AdditionalPrompt
		}
	}
	if additionalPromptCount == 0 && additionalPrompt != "" {
		return nil, nil, agent.NewError(agent.ErrorIntegrityFailure, "build model request", fmt.Errorf("system policy is missing its additional prompt placeholder"))
	}
	if additionalPromptCount == 1 {
		systemContent = strings.Replace(systemContent, "{{additional_prompt}}", additionalPrompt, 1)
	}
	systemContent = appendBasePromptContent(systemContent, basePrompt)
	messages := []completionMessage{{Role: "system", Content: systemContent}}
	for _, message := range request.Messages {
		if message.Provenance == agent.ProvenanceBasePrompt {
			continue
		}
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

func appendBasePromptContent(systemPolicy, basePrompt string) string {
	if basePrompt == "" {
		return systemPolicy
	}
	const additionalOpenTag = "<additional>"
	insertionIndex := strings.Index(systemPolicy, additionalOpenTag)
	if insertionIndex < 0 {
		const rootCloseTag = "</main>"
		closingIndex := strings.LastIndex(systemPolicy, rootCloseTag)
		if closingIndex < 0 || strings.TrimSpace(systemPolicy[closingIndex+len(rootCloseTag):]) != "" {
			return systemPolicy + "\n\n" + basePrompt
		}
		insertionIndex = closingIndex
	}
	return strings.TrimRight(systemPolicy[:insertionIndex], "\r\n") + "\n\n" + basePrompt + "\n\n" + systemPolicy[insertionIndex:]
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

func completionTools(request agent.ModelRequest, registry *command.Registry) ([]completionTool, error) {
	capabilities := request.Capabilities.Values()
	if request.CurrentMessageID.IsZero() {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "build model tools", fmt.Errorf("current message identity is required for tools"))
	}
	contextIDs := sortedContextMessageIDs(request.ContextMessages)
	replyContextIDs := append([]string{"none"}, contextIDs...)
	commandDescription := "No registered command is available for this invocation; use null."
	if len(request.Commands) > 0 {
		available := make([]string, 0, len(request.Commands))
		descriptors := registry.Descriptors()
		byName := make(map[string]command.Descriptor, len(descriptors))
		for _, descriptor := range descriptors {
			byName[string(descriptor.Name)] = descriptor
		}
		for _, name := range request.Commands {
			descriptor, ok := byName[name]
			if !ok {
				return nil, agent.NewError(agent.ErrorIntegrityFailure, "build model tools", fmt.Errorf("command is not registered"))
			}
			available = append(available, fmt.Sprintf("/%s - %s", name, descriptor.Description))
		}
		commandDescription = "Each item must be one complete registered command, with or without the initial slash. Available commands: " + strings.Join(available, "; ")
	}
	replyParameters, err := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"context_msg_id": map[string]any{
				"type":        "string",
				"enum":        replyContextIDs,
				"description": "Use none for an ordinary reply; otherwise use one exact six-digit ID from the supplied compact history.",
			},
			"text": map[string]any{
				"type":        "string",
				"minLength":   1,
				"description": "Visible reply text. For a person mention, copy an exact canonical `@Name (senderRef)` already shown in the transcript, or construct it from one `Name 【senderRef】` sender line; for example `Budi 【a1b2c3】` becomes `@Budi (a1b2c3)`. Never write bare `@Budi`, `@a1b2c3`, or `Budi (@a1b2c3)`. Special forms are `@all (all)` and the bot mention `@<assistant name> (bot)` using the configured assistant name from the system prompt.",
			},
			"command": map[string]any{
				"type":        []string{"array", "null"},
				"items":       map[string]any{"type": "string"},
				"maxItems":    8,
				"description": commandDescription,
			},
			"command_context_msg_id": map[string]any{
				"type":        []string{"array", "null"},
				"items":       map[string]any{"type": "string", "enum": replyContextIDs},
				"maxItems":    8,
				"description": "Optional per-command anchors; use none when a command has no message anchor.",
			},
		},
		"required":             []string{"context_msg_id", "text", "command", "command_context_msg_id"},
		"additionalProperties": false,
	})
	if err != nil {
		return nil, agent.NewError(agent.ErrorInternal, "build model tools", err)
	}
	tools := []completionTool{{Type: "function", Function: completionFunction{
		Name:        "reply_message",
		Description: "Return the visible reply and optionally request authorized group commands. Inline person mentions must use an exact canonical `@Name (senderRef)` from the transcript or use both values from the same sender line. Use only exact compact history IDs supplied in the schema; use none for an ordinary reply. Commands are separately parsed and authorized.",
		Parameters:  replyParameters,
	}}}
	for _, capability := range capabilities {
		name, description, parameters, ok := toolSchema(capability, contextIDs)
		if !ok {
			// Group command capabilities authorize command strings carried by
			// reply_message. They are deliberately not standalone provider tools.
			continue
		}
		tools = append(tools, completionTool{Type: "function", Function: completionFunction{Name: name, Description: description, Parameters: parameters}})
	}
	return tools, nil
}

func sortedContextMessageIDs(contextMessages map[string]identity.MessageID) []string {
	ids := make([]string, 0, len(contextMessages))
	for id := range contextMessages {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func toolSchema(capability agent.Capability, contextIDs []string) (string, string, json.RawMessage, bool) {
	switch capability {
	case "message.react":
		contextProperty := map[string]any{"type": "string", "minLength": 6, "maxLength": 6}
		if len(contextIDs) > 0 {
			contextProperty["enum"] = contextIDs
		}
		parameters, err := json.Marshal(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"context_msg_id": contextProperty,
				"emoji":          map[string]any{"type": "string", "maxLength": 64},
			},
			"required":             []string{"context_msg_id", "emoji"},
			"additionalProperties": false,
		})
		if err != nil {
			return "", "", nil, false
		}
		return "react_to_message", "React to one message from the supplied compact history. Use only an exact six-digit history ID from the schema.", parameters, true
	default:
		return "", "", nil, false
	}
}

func decodeModelOutput(content string, raw json.RawMessage, request agent.ModelRequest, registry *command.Registry) (string, identity.MessageID, []agent.ModelEffect, error) {
	if len(raw) == 0 || string(raw) == "null" || string(raw) == "[]" {
		return content, identity.MessageID{}, nil, nil
	}
	if len(request.Capabilities.Values()) == 0 {
		return "", identity.MessageID{}, nil, agent.NewError(agent.ErrorUnsupported, "decode model response", fmt.Errorf("model returned tools without granted capabilities"))
	}
	if request.CurrentMessageID.IsZero() {
		return "", identity.MessageID{}, nil, agent.NewError(agent.ErrorIntegrityFailure, "decode model response", fmt.Errorf("tool response has no current message target"))
	}
	var calls []completionToolCall
	if err := json.Unmarshal(raw, &calls); err != nil {
		return "", identity.MessageID{}, nil, agent.NewError(agent.ErrorProviderFailure, "decode model response", err)
	}
	if len(calls) == 0 || len(calls) > agent.MaxModelEffects {
		return "", identity.MessageID{}, nil, agent.NewError(agent.ErrorProviderFailure, "decode model response", fmt.Errorf("tool calls are invalid or exceed the limit"))
	}
	allowed := make(map[agent.Capability]struct{}, len(request.Capabilities.Values()))
	for _, capability := range request.Capabilities.Values() {
		allowed[capability] = struct{}{}
	}
	seen := make(map[string]struct{}, len(calls))
	effects := make([]agent.ModelEffect, 0, len(calls))
	replyText := content
	var replyTo identity.MessageID
	replySeen := false
	for _, call := range calls {
		if call.Type != "function" || strings.TrimSpace(call.ID) != call.ID || call.ID == "" || len(call.ID) > 128 {
			return "", identity.MessageID{}, nil, agent.NewError(agent.ErrorProviderFailure, "decode model response", fmt.Errorf("tool call identity is invalid"))
		}
		if _, exists := seen[call.ID]; exists {
			return "", identity.MessageID{}, nil, agent.NewError(agent.ErrorProviderFailure, "decode model response", fmt.Errorf("tool call ID is duplicated"))
		}
		seen[call.ID] = struct{}{}
		if call.Function.Name == "reply_message" {
			if replySeen {
				return "", identity.MessageID{}, nil, agent.NewError(agent.ErrorProviderFailure, "decode model response", fmt.Errorf("reply_message may only be called once"))
			}
			text, target, commands, err := decodeReplyMessage(call, request, registry)
			if err != nil {
				return "", identity.MessageID{}, nil, err
			}
			if strings.TrimSpace(content) != "" && content != text {
				return "", identity.MessageID{}, nil, agent.NewError(agent.ErrorProviderFailure, "decode model response", fmt.Errorf("content conflicts with reply_message text"))
			}
			replyText, replyTo, replySeen = text, target, true
			effects = append(effects, commands...)
			continue
		}
		intent, capability, err := decodeToolIntent(call.Function, request)
		if err != nil {
			return "", identity.MessageID{}, nil, err
		}
		if _, exists := allowed[capability]; !exists {
			return "", identity.MessageID{}, nil, agent.NewError(agent.ErrorPermissionDenied, "decode model response", fmt.Errorf("tool capability was not granted"))
		}
		effects = append(effects, agent.ModelEffect{CallID: call.ID, Intent: intent})
	}
	return replyText, replyTo, effects, nil
}

func decodeToolIntent(function completionFunction, request agent.ModelRequest) (agent.EffectIntent, agent.Capability, error) {
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
	case "react_to_message":
		var args struct {
			ContextMessageID string `json:"context_msg_id"`
			Emoji            string `json:"emoji"`
		}
		if err := decode(&args); err != nil {
			return agent.EffectIntent{}, "", err
		}
		target, ok := request.ContextMessages[args.ContextMessageID]
		if !ok {
			return agent.EffectIntent{}, "", agent.NewError(agent.ErrorProviderFailure, "decode model response", fmt.Errorf("reaction context message is not in the supplied history"))
		}
		return agent.EffectIntent{Kind: agent.EffectReact, TargetMessageID: target, Emoji: args.Emoji}, "message.react", nil
	default:
		return agent.EffectIntent{}, "", agent.NewError(agent.ErrorProviderFailure, "decode model response", fmt.Errorf("tool name is not declared"))
	}
}

func decodeReplyMessage(call completionToolCall, request agent.ModelRequest, registry *command.Registry) (string, identity.MessageID, []agent.ModelEffect, error) {
	var raw struct {
		ContextMessageID        string          `json:"context_msg_id"`
		Text                    string          `json:"text"`
		Commands                *[]string       `json:"command"`
		CommandContextMessageID json.RawMessage `json:"command_context_msg_id"`
	}
	if err := decodeArguments(call.Function.Arguments, &raw); err != nil {
		return "", identity.MessageID{}, nil, err
	}
	var commandContextMessageID *[]string
	if len(raw.CommandContextMessageID) > 0 {
		var arr []string
		if err := json.Unmarshal(raw.CommandContextMessageID, &arr); err == nil {
			commandContextMessageID = &arr
		} else {
			var str string
			if err := json.Unmarshal(raw.CommandContextMessageID, &str); err == nil {
				commandContextMessageID = &[]string{str}
			}
		}
	}
	args := struct {
		ContextMessageID        string
		Text                    string
		Commands                *[]string
		CommandContextMessageID *[]string
	}{
		ContextMessageID:        raw.ContextMessageID,
		Text:                    raw.Text,
		Commands:                raw.Commands,
		CommandContextMessageID: commandContextMessageID,
	}
	if strings.TrimSpace(args.Text) == "" || len(args.Text) > agent.MaxResponseBytes {
		return "", identity.MessageID{}, nil, agent.NewError(agent.ErrorProviderFailure, "decode reply_message", fmt.Errorf("reply text is invalid"))
	}
	var replyTo identity.MessageID
	if args.ContextMessageID != "none" {
		replyTo = request.ContextMessages[args.ContextMessageID]
	}
	if args.Commands == nil || len(*args.Commands) == 0 {
		// Unknown anchors do not suppress a visible reply; they simply omit the quote.
		return args.Text, replyTo, nil, nil
	}
	commands := *args.Commands
	if len(commands) > agent.MaxModelEffects {
		return "", identity.MessageID{}, nil, agent.NewError(agent.ErrorProviderFailure, "decode reply_message", fmt.Errorf("commands exceed the allowed limit"))
	}
	allowed := make(map[string]struct{}, len(request.Commands))
	for _, name := range request.Commands {
		allowed[name] = struct{}{}
	}
	effects := make([]agent.ModelEffect, 0, len(commands))
	for index, commandText := range commands {
		commandText = strings.TrimSpace(commandText)
		if commandText != "" && !strings.HasPrefix(commandText, "/") {
			commandText = "/" + commandText
		}
		parsed, _, recognized := registry.Parse(commandText)
		if !recognized {
			// A malformed optional command must not suppress the visible reply.
			continue
		}
		if _, ok := allowed[string(parsed.Name)]; !ok {
			return "", identity.MessageID{}, nil, agent.NewError(agent.ErrorPermissionDenied, "decode reply_message", fmt.Errorf("registered command is not allowed for bot origin"))
		}
		canonical := "/" + string(parsed.Name)
		if parsed.ArgumentsPresent {
			canonical += " " + parsed.Arguments
		}
		var commandTarget identity.MessageID
		if args.CommandContextMessageID != nil && index < len(*args.CommandContextMessageID) {
			contextRef := (*args.CommandContextMessageID)[index]
			if contextRef == "none" {
				commandTarget = identity.MessageID{}
			} else {
				var ok bool
				commandTarget, ok = request.ContextMessages[contextRef]
				if !ok {
					return "", identity.MessageID{}, nil, agent.NewError(agent.ErrorProviderFailure, "decode reply_message", fmt.Errorf("command context is not in the supplied history"))
				}
			}
		} else if args.ContextMessageID != "none" {
			var ok bool
			commandTarget, ok = request.ContextMessages[args.ContextMessageID]
			if !ok {
				return "", identity.MessageID{}, nil, agent.NewError(agent.ErrorProviderFailure, "decode reply_message", fmt.Errorf("reply context is not in the supplied history"))
			}
		}
		effects = append(effects, agent.ModelEffect{CallID: fmt.Sprintf("%s:%d", call.ID, index), Intent: agent.EffectIntent{Kind: agent.EffectRunCommand, TargetMessageID: commandTarget, Command: canonical}})
	}
	return args.Text, replyTo, effects, nil
}

func decodeArguments(raw json.RawMessage, value any) error {
	if len(raw) > 4*1024 {
		return agent.NewError(agent.ErrorProviderFailure, "decode model response", fmt.Errorf("tool arguments exceed the allowed limit"))
	}
	var arguments string
	if err := json.Unmarshal(raw, &arguments); err != nil {
		return agent.NewError(agent.ErrorProviderFailure, "decode model response", err)
	}
	if len(arguments) > 4*1024 {
		return agent.NewError(agent.ErrorProviderFailure, "decode model response", fmt.Errorf("tool arguments exceed the allowed limit"))
	}
	decoder := json.NewDecoder(strings.NewReader(arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return agent.NewError(agent.ErrorProviderFailure, "decode model response", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return agent.NewError(agent.ErrorProviderFailure, "decode model response", fmt.Errorf("tool arguments contain extra values"))
	}
	return nil
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
	path := strings.TrimRight(parsed.Path, "/")
	if !strings.HasSuffix(path, "/chat/completions") {
		parsed.Path = path + "/chat/completions"
		parsed.RawPath = ""
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

func providerRequestError(err error) error {
	var requestError *url.Error
	if errors.As(err, &requestError) && requestError.Err != nil {
		// Preserve the native transport failure without echoing the configured
		// provider URL, which may itself contain deployment-sensitive routing.
		return requestError.Err
	}
	return err
}

func llmContextError(operation string, err error) error {
	if err == context.DeadlineExceeded {
		return agent.NewError(agent.ErrorTimeout, operation, err)
	}
	return agent.NewError(agent.ErrorCancelled, operation, err)
}
