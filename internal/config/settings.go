package config

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

type ChatDefaults struct {
	ModerationLevel    uint8  `json:"moderationLevel"`
	PromptMode         string `json:"promptMode"`
	PromptText         string `json:"promptText"`
	TriggerMention     bool   `json:"triggerMention"`
	TriggerName        bool   `json:"triggerName"`
	TriggerReply       bool   `json:"triggerReply"`
	TriggerNameRegex   bool   `json:"triggerNameRegex"`
	TriggerNamePattern string `json:"triggerNamePattern"`
}

func DefaultChatDefaults() ChatDefaults {
	return ChatDefaults{TriggerMention: true, TriggerReply: true, PromptMode: "append"}
}

func (defaults ChatDefaults) Triggers() agent.TriggerConfig {
	return agent.TriggerConfig{Mention: defaults.TriggerMention, Name: defaults.TriggerName, Reply: defaults.TriggerReply, NameRegex: defaults.TriggerNameRegex, NamePattern: defaults.TriggerNamePattern}
}

func (defaults ChatDefaults) PromptOverride() *agent.PromptOverride {
	if strings.TrimSpace(defaults.PromptText) == "" {
		return nil
	}
	mode := agent.PromptAppend
	if defaults.PromptMode == "replace" {
		mode = agent.PromptReplace
	}
	return &agent.PromptOverride{Mode: mode, Text: defaults.PromptText}
}

func (defaults ChatDefaults) Validate() error {
	if !agent.ModerationLevel(defaults.ModerationLevel).Valid() {
		return fmt.Errorf("moderation level must be 0-3")
	}
	if defaults.PromptMode != "append" && defaults.PromptMode != "replace" {
		return fmt.Errorf("prompt mode must be append or replace")
	}
	if len(defaults.PromptText) > agent.MaxPromptBytes {
		return fmt.Errorf("prompt text is too long")
	}
	return defaults.Triggers().Validate()
}

// Settings is the typed, persisted application configuration. It deliberately
// has no dependency on environment variables, SQL, or Wails. Secret fields are
// only used by backend code; Public returns a copy suitable for UI responses.
// TenantID and AccountID are bootstrap values used when constructing a runtime
// snapshot. They are identity-file values, rather than editable settings.
type Settings struct {
	AssistantName string
	BasePrompt    string
	ChatDefaults  ChatDefaults

	WhatsAppEnabled bool
	AgentEnabled    bool
	OwnerJID        string
	ChatAllowlist   []string

	LLMEndpoint      string
	LLMAPIKey        string
	LLMModel         string
	LLMProviderID    string
	FallbackEndpoint string
	FallbackAPIKey   string

	LLMTimeout       time.Duration
	LLMConcurrency   uint32
	MaxOutputTokens  uint32
	MaxResponseBytes uint32

	HistoryWindow     uint32
	MaxContextBytes   uint32
	HistoryKeepLatest uint32
	HistoryMaxAge     time.Duration

	InboundQueue   uint32
	InboundWorkers uint32
	CommandQueue   uint32
	CommandWorkers uint32
	AIQueue        uint32
	AIWorkers      uint32

	MessageDebounce time.Duration
	MessageBurstCap uint32

	AgentMaxLive             uint32
	AgentIdleTTL             time.Duration
	AgentConstructionTimeout time.Duration
	ConnectTimeout           time.Duration
	SendTimeout              time.Duration
	ShutdownTimeout          time.Duration

	PolicyID        string
	PolicyRevision  uint64
	LogLevel        string
	LogFormat       string
	LangSmithAPIKey string

	DataDir       string
	EnvFile       string
	HTTPAddress   string
	PairingOutput string
	NoColor       bool
	ForceColor    bool
	StartOnLaunch bool

	TenantID  identity.TenantID
	AccountID identity.AccountID
}

// PublicSettings is safe to send to a settings form. Secrets are represented
// by configured flags and are never copied into this value.
type PublicSettings struct {
	Settings
	LLMAPIKeyConfigured       bool
	FallbackAPIKeyConfigured  bool
	LangSmithAPIKeyConfigured bool
}

// Public returns settings with all secret values removed.
func (settings Settings) Public() PublicSettings {
	public := PublicSettings{
		Settings:                  settings.clone(),
		LLMAPIKeyConfigured:       strings.TrimSpace(settings.LLMAPIKey) != "",
		FallbackAPIKeyConfigured:  strings.TrimSpace(settings.FallbackAPIKey) != "",
		LangSmithAPIKeyConfigured: strings.TrimSpace(settings.LangSmithAPIKey) != "",
	}
	public.LLMAPIKey = ""
	public.FallbackAPIKey = ""
	public.LangSmithAPIKey = ""
	return public
}

func (settings Settings) PublicValues() PublicSettings { return settings.Public() }

// DefaultSettings returns the GUI/settings defaults. Required Agent values are
// intentionally empty: a new installation can persist a draft and pair in
// session-only mode before configuring the Agent.
func DefaultSettings() Settings {
	return Settings{
		DataDir: defaultDataDir, HTTPAddress: defaultHTTPAddress,
		LogLevel: defaultLogLevel, LogFormat: defaultLogFormat,
		ShutdownTimeout: defaultShutdownTimeout,
		WhatsAppEnabled: defaultWhatsAppEnabled, AgentEnabled: defaultWhatsAppEnabled,
		LLMProviderID: defaultProviderID, PolicyID: defaultPolicyID, PolicyRevision: 1,
		LLMTimeout: defaultLLMTimeout, LLMConcurrency: defaultLLMConcurrency,
		MaxOutputTokens: defaultMaxOutputTokens, MaxResponseBytes: defaultMaxResponseBytes,
		InboundQueue: defaultInboundQueue, InboundWorkers: defaultInboundWorkers,
		CommandQueue: defaultCommandQueue, CommandWorkers: defaultCommandWorkers,
		AIQueue: defaultAIQueue, AIWorkers: defaultAIWorkers,
		MessageDebounce: defaultMessageDebounce, MessageBurstCap: defaultMessageBurstCap,
		HistoryWindow: defaultHistoryWindow, MaxContextBytes: defaultMaxContextBytes,
		HistoryKeepLatest: defaultHistoryKeepLatest, HistoryMaxAge: defaultHistoryMaxAge,
		AgentMaxLive: defaultRegistryMaxLive, AgentIdleTTL: defaultRegistryIdleTTL,
		AgentConstructionTimeout: defaultConstructionTimeout,
		ConnectTimeout:           defaultConnectTimeout, SendTimeout: defaultSendTimeout,
		PairingOutput: defaultPairingOutput,
		StartOnLaunch: false,
		ChatDefaults:  DefaultChatDefaults(),
	}
}

// SessionContext supplies the data-root and identity state available to a
// session controller. It is optional for validation of a new, unpaired draft.
type SessionContext struct {
	DataDir       string
	TenantID      identity.TenantID
	AccountID     identity.AccountID
	IdentityReady bool
}

// ReadinessIssue is a safe, user-facing reason why a mode cannot start yet.
// Messages never include configured values or secret material.
type ReadinessIssue struct {
	Field   string
	Code    string
	Message string
}

// ValidationIssues is returned by the issue-oriented helpers. Validation
// helpers below also expose error forms for callers that only need a gate.
type ValidationIssues []ReadinessIssue

func (issues ValidationIssues) Error() string {
	if len(issues) == 0 {
		return ""
	}
	parts := make([]string, 0, len(issues))
	for _, issue := range issues {
		if issue.Field == "" {
			parts = append(parts, issue.Message)
		} else {
			parts = append(parts, issue.Field+": "+issue.Message)
		}
	}
	return strings.Join(parts, "; ")
}

// ValidateDraft rejects malformed values but permits omitted setup values.
// This is the validation used before Save on first launch.
func ValidateDraft(settings Settings) error {
	settings = settings.withDefaults()
	issues := draftIssues(settings)
	if len(issues) == 0 {
		return nil
	}
	return ValidationIssues(issues)
}

// ValidateSession validates values needed to open WhatsApp in either pairing
// or Agent mode. When context is supplied, it also validates the data root and
// identity state without touching the filesystem.
func ValidateSession(settings Settings, context ...SessionContext) error {
	settings = settings.withDefaults()
	issues := draftIssues(settings)
	if strings.TrimSpace(settings.DataDir) == "" {
		issues = append(issues, ReadinessIssue{Field: "WAZZAP_DATA_DIR", Code: "required", Message: "data directory is required"})
	} else if _, err := resolveDataDir(settings.DataDir); err != nil {
		issues = append(issues, ReadinessIssue{Field: "WAZZAP_DATA_DIR", Code: "invalid", Message: "data directory is invalid"})
	}
	if len(context) > 0 {
		state := context[0]
		if strings.TrimSpace(state.DataDir) == "" && strings.TrimSpace(settings.DataDir) == "" {
			issues = append(issues, ReadinessIssue{Field: "WAZZAP_DATA_DIR", Code: "required", Message: "data directory is required"})
		}
		if state.IdentityReady && (state.TenantID.IsZero() || state.AccountID.IsZero()) {
			issues = append(issues, ReadinessIssue{Field: "identity", Code: "invalid", Message: "runtime identity is incomplete"})
		}
	}
	if len(issues) == 0 {
		return nil
	}
	return ValidationIssues(issues)
}

// ValidateAgent is the strict readiness gate for the bot pipeline.
func ValidateAgent(settings Settings) error {
	settings = settings.withDefaults()
	issues := agentIssues(settings)
	if len(issues) == 0 {
		return nil
	}
	return ValidationIssues(issues)
}

// DraftReadinessIssues reports malformed draft values and is useful to form
// controllers that need structured field errors.
func DraftReadinessIssues(settings Settings) []ReadinessIssue {
	return cloneIssues(draftIssues(settings))
}

// SessionReadinessIssues reports session validation issues without I/O.
func SessionReadinessIssues(settings Settings, context ...SessionContext) []ReadinessIssue {
	if err := ValidateSession(settings, context...); err == nil {
		return nil
	} else if issues, ok := err.(ValidationIssues); ok {
		return cloneIssues(issues)
	}
	return []ReadinessIssue{{Code: "invalid", Message: "settings are invalid"}}
}

// AgentReadinessIssues reports the fields preventing Agent startup.
func AgentReadinessIssues(settings Settings) []ReadinessIssue {
	return cloneIssues(agentIssues(settings))
}

// ReadinessIssues combines draft, session, and (when enabled) Agent readiness.
func ReadinessIssues(settings Settings, context ...SessionContext) []ReadinessIssue {
	issues := SessionReadinessIssues(settings, context...)
	if settings.AgentEnabled {
		issues = append(issues, agentIssues(settings)...)
	}
	return cloneIssues(issues)
}

// Snapshot converts typed settings to the existing immutable runtime snapshot.
// It performs validation only and never reads identity files, the environment,
// or any other external state.
func SnapshotFromSettings(settings Settings) (Snapshot, error) {
	settings = settings.withDefaults()
	if err := ValidateSession(settings); err != nil {
		return Snapshot{}, err
	}
	if settings.AgentEnabled {
		if err := ValidateAgent(settings); err != nil {
			return Snapshot{}, err
		}
	}
	dataDir, err := resolveDataDir(settings.DataDir)
	if err != nil {
		return Snapshot{}, fmt.Errorf("WAZZAP_DATA_DIR: %w", err)
	}
	providerID, err := identity.ParseProviderID(nonempty(settings.LLMProviderID, defaultProviderID))
	if err != nil {
		return Snapshot{}, fmt.Errorf("WAZZAP_LLM_PROVIDER_ID: %w", err)
	}
	policyID, err := identity.ParsePolicyID(nonempty(settings.PolicyID, defaultPolicyID))
	if err != nil {
		return Snapshot{}, fmt.Errorf("WAZZAP_POLICY_ID: %w", err)
	}
	return Snapshot{
		dataDir: dataDir, httpAddress: nonempty(settings.HTTPAddress, defaultHTTPAddress),
		logLevel:        nonempty(strings.ToLower(settings.LogLevel), defaultLogLevel),
		logFormat:       nonempty(strings.ToLower(settings.LogFormat), defaultLogFormat),
		shutdownTimeout: settings.ShutdownTimeout, whatsAppEnabled: settings.WhatsAppEnabled,
		agentEnabled: settings.AgentEnabled, tenantID: settings.TenantID, accountID: settings.AccountID,
		ownerAddress: strings.TrimSpace(settings.OwnerJID), allowlist: cloneStrings(settings.ChatAllowlist),
		llmEndpoint: strings.TrimSpace(settings.LLMEndpoint), llmAPIKey: settings.LLMAPIKey,
		llmFallbackEndpoint: strings.TrimSpace(settings.FallbackEndpoint), llmFallbackAPIKey: settings.FallbackAPIKey,
		langsmithAPIKey: settings.LangSmithAPIKey, llmModel: strings.TrimSpace(settings.LLMModel),
		llmProviderID: providerID, llmTimeout: settings.LLMTimeout, llmConcurrency: settings.LLMConcurrency,
		maxOutputTokens: settings.MaxOutputTokens, maxResponseBytes: settings.MaxResponseBytes,
		basePrompt: settings.BasePrompt, policyID: policyID, policyRevision: settings.PolicyRevision,
		chatDefaults: settings.ChatDefaults,
		inboundQueue: settings.InboundQueue, inboundWorkers: settings.InboundWorkers,
		commandQueue: settings.CommandQueue, commandWorkers: settings.CommandWorkers,
		aiQueue: settings.AIQueue, aiWorkers: settings.AIWorkers,
		messageDebounce: settings.MessageDebounce, messageBurstCap: settings.MessageBurstCap,
		historyWindow: settings.HistoryWindow, maxContextBytes: settings.MaxContextBytes,
		historyKeepLatest: settings.HistoryKeepLatest, historyMaxAge: settings.HistoryMaxAge,
		registryMaxLive: settings.AgentMaxLive, registryIdleTTL: settings.AgentIdleTTL,
		constructionTimeout: settings.AgentConstructionTimeout, connectTimeout: settings.ConnectTimeout,
		sendTimeout: settings.SendTimeout, pairingOutput: nonempty(strings.ToLower(settings.PairingOutput), defaultPairingOutput),
		assistantName: strings.TrimSpace(settings.AssistantName),
	}, nil
}

// SessionSnapshotFromSettings creates the reduced snapshot needed for a
// WhatsApp-only session. Pairing and resume do not require Agent, owner, LLM,
// prompt, or allowlist readiness. The WhatsApp path and shared connection
// settings still use the same typed validation/defaults as the full runtime.
func SessionSnapshotFromSettings(settings Settings) (Snapshot, error) {
	settings.WhatsAppEnabled = true
	settings.AgentEnabled = false
	return SnapshotFromSettings(settings)
}

// SessionSnapshotWithIdentity builds a session-only snapshot for the durable
// identity selected by the session controller. The account scope is persisted
// in settings.db so a later pairing can isolate application data by account.
func SessionSnapshotWithIdentity(settings Settings, tenantID identity.TenantID, accountID identity.AccountID) (Snapshot, error) {
	if tenantID.IsZero() || accountID.IsZero() {
		return Snapshot{}, fmt.Errorf("session identity is incomplete")
	}
	settings.TenantID = tenantID
	settings.AccountID = accountID
	return SessionSnapshotFromSettings(settings)
}

// Snapshot and ToSnapshot are method forms convenient for composition code.
func (settings Settings) Snapshot() (Snapshot, error)   { return SnapshotFromSettings(settings) }
func (settings Settings) ToSnapshot() (Snapshot, error) { return SnapshotFromSettings(settings) }
func (settings Settings) ValidateDraft() error          { return ValidateDraft(settings) }
func (settings Settings) ValidateSession(context ...SessionContext) error {
	return ValidateSession(settings, context...)
}
func (settings Settings) ValidateAgent() error { return ValidateAgent(settings) }
func (settings Settings) ReadinessIssues(context ...SessionContext) []ReadinessIssue {
	return ReadinessIssues(settings, context...)
}

type FieldKind string

const (
	FieldString   FieldKind = "string"
	FieldBool     FieldKind = "bool"
	FieldDuration FieldKind = "duration"
	FieldUint     FieldKind = "uint"
	FieldSecret   FieldKind = "secret"
)

// FieldDescriptor describes one canonical settings key. ReadOnly fields are
// included so UI/catalog tests can prove that no loader key is silently lost.
type FieldDescriptor struct {
	Key       string
	Group     string
	Kind      FieldKind
	Sensitive bool
	ReadOnly  bool
	CLIOnly   bool
	Default   string
}

// SettingsSchema returns a fresh field catalog on every call.
func SettingsSchema() []FieldDescriptor {
	return []FieldDescriptor{
		{Key: "ASSISTANT_NAME", Group: "assistant", Kind: FieldString}, {Key: "WAZZAP_BASE_PROMPT", Group: "assistant", Kind: FieldString},
		{Key: "WAZZAP_WHATSAPP_ENABLED", Group: "activation", Kind: FieldBool, Default: "true"}, {Key: "WAZZAP_AGENT_ENABLED", Group: "activation", Kind: FieldBool, Default: "true"},
		{Key: "WAZZAP_OWNER_JID", Group: "access", Kind: FieldString}, {Key: "WAZZAP_CHAT_ALLOWLIST", Group: "access", Kind: FieldString},
		{Key: "WAZZAP_LLM_ENDPOINT", Group: "provider", Kind: FieldString}, {Key: "WAZZAP_LLM_API_KEY", Group: "provider", Kind: FieldSecret, Sensitive: true}, {Key: "WAZZAP_LLM_MODEL", Group: "provider", Kind: FieldString}, {Key: "WAZZAP_LLM_PROVIDER_ID", Group: "provider", Kind: FieldString, Default: defaultProviderID},
		{Key: "WAZZAP_LLM_FALLBACK_ENDPOINT", Group: "fallback", Kind: FieldString}, {Key: "WAZZAP_LLM_FALLBACK_API_KEY", Group: "fallback", Kind: FieldSecret, Sensitive: true},
		{Key: "WAZZAP_LLM_TIMEOUT", Group: "limits", Kind: FieldDuration, Default: defaultLLMTimeout.String()}, {Key: "WAZZAP_LLM_CONCURRENCY", Group: "limits", Kind: FieldUint, Default: fmt.Sprint(defaultLLMConcurrency)}, {Key: "WAZZAP_MAX_OUTPUT_TOKENS", Group: "limits", Kind: FieldUint, Default: fmt.Sprint(defaultMaxOutputTokens)}, {Key: "WAZZAP_MAX_RESPONSE_BYTES", Group: "limits", Kind: FieldUint, Default: fmt.Sprint(defaultMaxResponseBytes)},
		{Key: "WAZZAP_HISTORY_WINDOW", Group: "context", Kind: FieldUint, Default: fmt.Sprint(defaultHistoryWindow)}, {Key: "WAZZAP_MAX_CONTEXT_BYTES", Group: "context", Kind: FieldUint, Default: fmt.Sprint(defaultMaxContextBytes)}, {Key: "WAZZAP_HISTORY_KEEP_LATEST", Group: "retention", Kind: FieldUint, Default: fmt.Sprint(defaultHistoryKeepLatest)}, {Key: "WAZZAP_HISTORY_MAX_AGE", Group: "retention", Kind: FieldDuration, Default: defaultHistoryMaxAge.String()},
		{Key: "WAZZAP_INBOUND_QUEUE", Group: "inbound", Kind: FieldUint, Default: fmt.Sprint(defaultInboundQueue)}, {Key: "WAZZAP_INBOUND_WORKERS", Group: "inbound", Kind: FieldUint, Default: fmt.Sprint(defaultInboundWorkers)}, {Key: "WAZZAP_COMMAND_QUEUE", Group: "command", Kind: FieldUint, Default: fmt.Sprint(defaultCommandQueue)}, {Key: "WAZZAP_COMMAND_WORKERS", Group: "command", Kind: FieldUint, Default: fmt.Sprint(defaultCommandWorkers)}, {Key: "WAZZAP_AI_QUEUE", Group: "ai", Kind: FieldUint, Default: fmt.Sprint(defaultAIQueue)}, {Key: "WAZZAP_AI_WORKERS", Group: "ai", Kind: FieldUint, Default: fmt.Sprint(defaultAIWorkers)},
		{Key: "WAZZAP_MESSAGE_DEBOUNCE", Group: "batching", Kind: FieldDuration, Default: defaultMessageDebounce.String()}, {Key: "WAZZAP_MESSAGE_BURST_CAP", Group: "batching", Kind: FieldUint, Default: fmt.Sprint(defaultMessageBurstCap)},
		{Key: "WAZZAP_AGENT_MAX_LIVE", Group: "registry", Kind: FieldUint, Default: fmt.Sprint(defaultRegistryMaxLive)}, {Key: "WAZZAP_AGENT_IDLE_TTL", Group: "registry", Kind: FieldDuration, Default: defaultRegistryIdleTTL.String()}, {Key: "WAZZAP_AGENT_CONSTRUCTION_TIMEOUT", Group: "registry", Kind: FieldDuration, Default: defaultConstructionTimeout.String()},
		{Key: "WAZZAP_CONNECT_TIMEOUT", Group: "connection", Kind: FieldDuration, Default: defaultConnectTimeout.String()}, {Key: "WAZZAP_SEND_TIMEOUT", Group: "connection", Kind: FieldDuration, Default: defaultSendTimeout.String()}, {Key: "WAZZAP_SHUTDOWN_TIMEOUT", Group: "connection", Kind: FieldDuration, Default: defaultShutdownTimeout.String()},
		{Key: "WAZZAP_POLICY_ID", Group: "policy", Kind: FieldString, Default: defaultPolicyID}, {Key: "WAZZAP_POLICY_REVISION", Group: "policy", Kind: FieldUint, Default: "1"},
		{Key: "WAZZAP_LOG_LEVEL", Group: "observability", Kind: FieldString, Default: defaultLogLevel}, {Key: "WAZZAP_LOG_FORMAT", Group: "observability", Kind: FieldString, Default: defaultLogFormat}, {Key: "LANGSMITH_API_KEY", Group: "observability", Kind: FieldSecret, Sensitive: true},
		{Key: "WAZZAP_DATA_DIR", Group: "storage", Kind: FieldString, Default: defaultDataDir}, {Key: "WAZZAP_ENV_FILE", Group: "source", Kind: FieldString, CLIOnly: true}, {Key: "WAZZAP_HTTP_ADDRESS", Group: "http", Kind: FieldString, CLIOnly: true, Default: defaultHTTPAddress}, {Key: "WAZZAP_PAIRING_OUTPUT", Group: "pairing", Kind: FieldString, CLIOnly: true, Default: defaultPairingOutput},
		{Key: "WAZZAP_TENANT_ID", Group: "identity", Kind: FieldString, ReadOnly: true}, {Key: "WAZZAP_ACCOUNT_ID", Group: "identity", Kind: FieldString, ReadOnly: true}, {Key: "NO_COLOR", Group: "terminal", Kind: FieldBool, CLIOnly: true}, {Key: "FORCE_COLOR", Group: "terminal", Kind: FieldBool, CLIOnly: true},
		{Key: "startOnLaunch", Group: "application", Kind: FieldBool, Default: "false"},
	}
}

func (settings Settings) clone() Settings {
	settings.ChatAllowlist = cloneStrings(settings.ChatAllowlist)
	return settings
}
func (settings Settings) withDefaults() Settings {
	defaults := DefaultSettings()
	if strings.TrimSpace(settings.DataDir) == "" {
		settings.DataDir = defaults.DataDir
	}
	if strings.TrimSpace(settings.HTTPAddress) == "" {
		settings.HTTPAddress = defaults.HTTPAddress
	}
	if strings.TrimSpace(settings.LogLevel) == "" {
		settings.LogLevel = defaults.LogLevel
	}
	if strings.TrimSpace(settings.LogFormat) == "" {
		settings.LogFormat = defaults.LogFormat
	}
	if strings.TrimSpace(settings.LLMProviderID) == "" {
		settings.LLMProviderID = defaults.LLMProviderID
	}
	if strings.TrimSpace(settings.PolicyID) == "" {
		settings.PolicyID = defaults.PolicyID
	}
	if settings.PolicyRevision == 0 {
		settings.PolicyRevision = defaults.PolicyRevision
	}
	if settings.ChatDefaults.PromptMode == "" {
		settings.ChatDefaults.PromptMode = "append"
	}
	if settings.ShutdownTimeout == 0 {
		settings.ShutdownTimeout = defaults.ShutdownTimeout
	}
	if settings.LLMTimeout == 0 {
		settings.LLMTimeout = defaults.LLMTimeout
	}
	if settings.LLMConcurrency == 0 {
		settings.LLMConcurrency = defaults.LLMConcurrency
	}
	if settings.MaxOutputTokens == 0 {
		settings.MaxOutputTokens = defaults.MaxOutputTokens
	}
	if settings.MaxResponseBytes == 0 {
		settings.MaxResponseBytes = defaults.MaxResponseBytes
	}
	if settings.InboundQueue == 0 {
		settings.InboundQueue = defaults.InboundQueue
	}
	if settings.InboundWorkers == 0 {
		settings.InboundWorkers = defaults.InboundWorkers
	}
	if settings.CommandQueue == 0 {
		settings.CommandQueue = defaults.CommandQueue
	}
	if settings.CommandWorkers == 0 {
		settings.CommandWorkers = defaults.CommandWorkers
	}
	if settings.AIQueue == 0 {
		settings.AIQueue = defaults.AIQueue
	}
	if settings.AIWorkers == 0 {
		settings.AIWorkers = defaults.AIWorkers
	}
	if settings.MessageDebounce == 0 {
		settings.MessageDebounce = defaults.MessageDebounce
	}
	if settings.MessageBurstCap == 0 {
		settings.MessageBurstCap = defaults.MessageBurstCap
	}
	if settings.HistoryWindow == 0 {
		settings.HistoryWindow = defaults.HistoryWindow
	}
	if settings.MaxContextBytes == 0 {
		settings.MaxContextBytes = defaults.MaxContextBytes
	}
	if settings.HistoryKeepLatest == 0 {
		settings.HistoryKeepLatest = defaults.HistoryKeepLatest
	}
	if settings.HistoryMaxAge == 0 {
		settings.HistoryMaxAge = defaults.HistoryMaxAge
	}
	if settings.AgentMaxLive == 0 {
		settings.AgentMaxLive = defaults.AgentMaxLive
	}
	if settings.AgentIdleTTL == 0 {
		settings.AgentIdleTTL = defaults.AgentIdleTTL
	}
	if settings.AgentConstructionTimeout == 0 {
		settings.AgentConstructionTimeout = defaults.AgentConstructionTimeout
	}
	if settings.ConnectTimeout == 0 {
		settings.ConnectTimeout = defaults.ConnectTimeout
	}
	if settings.SendTimeout == 0 {
		settings.SendTimeout = defaults.SendTimeout
	}
	if strings.TrimSpace(settings.PairingOutput) == "" {
		settings.PairingOutput = defaults.PairingOutput
	}
	settings.ChatAllowlist = cloneStrings(settings.ChatAllowlist)
	return settings
}
func cloneStrings(values []string) []string { return append([]string(nil), values...) }
func cloneIssues(values []ReadinessIssue) []ReadinessIssue {
	return append([]ReadinessIssue(nil), values...)
}
func nonempty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func draftIssues(settings Settings) []ReadinessIssue {
	issues := make([]ReadinessIssue, 0)
	add := func(field, code, message string) {
		issues = append(issues, ReadinessIssue{Field: field, Code: code, Message: message})
	}
	if settings.WhatsAppEnabled == false && settings.AgentEnabled {
		add("WAZZAP_AGENT_ENABLED", "incompatible", "Agent requires WhatsApp to be enabled")
	}
	if settings.DataDir != "" {
		if _, err := resolveDataDir(settings.DataDir); err != nil {
			add("WAZZAP_DATA_DIR", "invalid", "data directory is invalid")
		}
	}
	if settings.HTTPAddress != "" {
		if err := validateHTTPAddress(settings.HTTPAddress); err != nil {
			add("WAZZAP_HTTP_ADDRESS", "invalid", "HTTP address is invalid")
		}
	}
	if settings.LogLevel != "" && !oneOf(strings.ToLower(settings.LogLevel), "debug", "info", "warn", "error") {
		add("WAZZAP_LOG_LEVEL", "invalid", "must be debug, info, warn, or error")
	}
	if settings.LogFormat != "" && !oneOf(strings.ToLower(settings.LogFormat), "json", "text", "compact") {
		add("WAZZAP_LOG_FORMAT", "invalid", "must be json, text, or compact")
	}
	if settings.PairingOutput != "" && !oneOf(strings.ToLower(settings.PairingOutput), "disabled", "terminal") {
		add("WAZZAP_PAIRING_OUTPUT", "invalid", "must be disabled or terminal")
	}
	if settings.LLMProviderID != "" {
		if _, err := identity.ParseProviderID(settings.LLMProviderID); err != nil {
			add("WAZZAP_LLM_PROVIDER_ID", "invalid", "provider ID is invalid")
		}
	}
	if settings.PolicyID != "" {
		if _, err := identity.ParsePolicyID(settings.PolicyID); err != nil {
			add("WAZZAP_POLICY_ID", "invalid", "policy ID is invalid")
		}
	}
	if settings.PolicyRevision == 0 {
		add("WAZZAP_POLICY_REVISION", "range", "must be greater than zero")
	}
	if err := settings.ChatDefaults.Validate(); err != nil {
		add("chatDefaults", "invalid", err.Error())
	}
	checkDuration := func(field string, value, maximum time.Duration) {
		if value <= 0 || value > maximum {
			add(field, "range", "must be greater than zero and within the supported limit")
		}
	}
	if settings.LLMTimeout != 0 {
		checkDuration("WAZZAP_LLM_TIMEOUT", settings.LLMTimeout, 10*time.Minute)
	}
	if settings.ShutdownTimeout != 0 {
		checkDuration("WAZZAP_SHUTDOWN_TIMEOUT", settings.ShutdownTimeout, maxShutdownTimeout)
	}
	if settings.ConnectTimeout != 0 {
		checkDuration("WAZZAP_CONNECT_TIMEOUT", settings.ConnectTimeout, 10*time.Minute)
	}
	if settings.SendTimeout != 0 {
		checkDuration("WAZZAP_SEND_TIMEOUT", settings.SendTimeout, 5*time.Minute)
	}
	if settings.AgentIdleTTL != 0 {
		checkDuration("WAZZAP_AGENT_IDLE_TTL", settings.AgentIdleTTL, 24*time.Hour)
	}
	if settings.AgentConstructionTimeout != 0 {
		checkDuration("WAZZAP_AGENT_CONSTRUCTION_TIMEOUT", settings.AgentConstructionTimeout, time.Minute)
	}
	if settings.MessageDebounce != 0 {
		checkDuration("WAZZAP_MESSAGE_DEBOUNCE", settings.MessageDebounce, time.Minute)
	}
	if settings.HistoryMaxAge != 0 {
		checkDuration("WAZZAP_HISTORY_MAX_AGE", settings.HistoryMaxAge, 10*365*24*time.Hour)
	}
	checkUint := func(field string, value, minimum, maximum uint32) {
		if value != 0 && (value < minimum || value > maximum) {
			add(field, "range", "is outside the supported range")
		}
	}
	checkUint("WAZZAP_LLM_CONCURRENCY", settings.LLMConcurrency, 1, 256)
	checkUint("WAZZAP_MAX_OUTPUT_TOKENS", settings.MaxOutputTokens, 1, 65536)
	checkUint("WAZZAP_MAX_RESPONSE_BYTES", settings.MaxResponseBytes, 1, maxResponseBytes)
	checkUint("WAZZAP_HISTORY_WINDOW", settings.HistoryWindow, 1, 256)
	checkUint("WAZZAP_MAX_CONTEXT_BYTES", settings.MaxContextBytes, 1, 1024*1024)
	checkUint("WAZZAP_HISTORY_KEEP_LATEST", settings.HistoryKeepLatest, 1, 1000000)
	checkUint("WAZZAP_INBOUND_QUEUE", settings.InboundQueue, 1, 65536)
	checkUint("WAZZAP_INBOUND_WORKERS", settings.InboundWorkers, 1, 256)
	checkUint("WAZZAP_COMMAND_QUEUE", settings.CommandQueue, 1, 65536)
	checkUint("WAZZAP_COMMAND_WORKERS", settings.CommandWorkers, 1, 256)
	checkUint("WAZZAP_AI_QUEUE", settings.AIQueue, 1, 65536)
	checkUint("WAZZAP_AI_WORKERS", settings.AIWorkers, 1, 256)
	checkUint("WAZZAP_MESSAGE_BURST_CAP", settings.MessageBurstCap, 1, 256)
	checkUint("WAZZAP_AGENT_MAX_LIVE", settings.AgentMaxLive, 1, 1000000)
	if settings.FallbackEndpoint != "" || settings.FallbackAPIKey != "" {
		if (strings.TrimSpace(settings.FallbackEndpoint) == "") != (strings.TrimSpace(settings.FallbackAPIKey) == "") {
			add("WAZZAP_LLM_FALLBACK_ENDPOINT", "incomplete", "fallback endpoint and key must be configured together")
		}
	}
	for _, item := range settings.ChatAllowlist {
		if strings.TrimSpace(item) == "" || validateOpaqueAddress(strings.TrimSpace(item)) != nil {
			add("WAZZAP_CHAT_ALLOWLIST", "invalid", "allowlist contains an invalid address")
			break
		}
	}
	return issues
}

func agentIssues(settings Settings) []ReadinessIssue {
	issues := draftIssues(settings)
	add := func(field, code, message string) {
		issues = append(issues, ReadinessIssue{Field: field, Code: code, Message: message})
	}
	if !settings.WhatsAppEnabled {
		add("WAZZAP_WHATSAPP_ENABLED", "required", "WhatsApp must be enabled for Agent mode")
	}
	if strings.TrimSpace(settings.AssistantName) == "" {
		add("ASSISTANT_NAME", "required", "assistant name is required for Agent mode")
	}
	if strings.TrimSpace(settings.OwnerJID) == "" {
		add("WAZZAP_OWNER_JID", "required", "owner JID is required for Agent mode")
	} else if validateOpaqueAddress(strings.TrimSpace(settings.OwnerJID)) != nil {
		add("WAZZAP_OWNER_JID", "invalid", "owner JID is invalid")
	}
	if len(settings.ChatAllowlist) == 0 {
		add("WAZZAP_CHAT_ALLOWLIST", "required", "at least one allowlist target is required for Agent mode")
	} else if len(settings.ChatAllowlist) > 1024 {
		add("WAZZAP_CHAT_ALLOWLIST", "range", "at most 1024 allowlist targets are allowed")
	}
	if strings.TrimSpace(settings.LLMEndpoint) == "" {
		add("WAZZAP_LLM_ENDPOINT", "required", "LLM endpoint is required for Agent mode")
	} else if !validHTTPURL(settings.LLMEndpoint) {
		add("WAZZAP_LLM_ENDPOINT", "invalid", "LLM endpoint must be an absolute HTTP(S) URL")
	}
	if strings.TrimSpace(settings.LLMAPIKey) == "" {
		add("WAZZAP_LLM_API_KEY", "required", "LLM API key is required for Agent mode")
	}
	if strings.TrimSpace(settings.LLMModel) == "" {
		add("WAZZAP_LLM_MODEL", "required", "LLM model is required for Agent mode")
	}
	if strings.TrimSpace(settings.BasePrompt) == "" {
		add("WAZZAP_BASE_PROMPT", "required", "base prompt is required for Agent mode")
	} else if len(settings.BasePrompt) > 16*1024 {
		add("WAZZAP_BASE_PROMPT", "range", "base prompt is too large")
	}
	if settings.FallbackEndpoint != "" && !validHTTPURL(settings.FallbackEndpoint) {
		add("WAZZAP_LLM_FALLBACK_ENDPOINT", "invalid", "fallback endpoint must be an absolute HTTP(S) URL")
	}
	return issues
}

func validHTTPURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	return err == nil && parsed.Host != "" && (parsed.Scheme == "http" || parsed.Scheme == "https")
}
