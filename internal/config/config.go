package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

const (
	defaultDataDir             = "./data"
	defaultHTTPAddress         = "127.0.0.1:8080"
	defaultLogLevel            = "info"
	defaultLogFormat           = "json"
	defaultShutdownTimeout     = 30 * time.Second
	defaultLLMTimeout          = 60 * time.Second
	defaultConnectTimeout      = 90 * time.Second
	defaultSendTimeout         = 30 * time.Second
	defaultRegistryIdleTTL     = 15 * time.Minute
	defaultConstructionTimeout = 10 * time.Second
	defaultInboundQueue        = 512
	defaultInboundWorkers      = 4
	defaultCommandQueue        = 128
	defaultCommandWorkers      = 2
	defaultAIQueue             = 512
	defaultAIWorkers           = 4
	defaultMessageDebounce     = 350 * time.Millisecond
	defaultMessageBurstCap     = 8
	defaultHistoryWindow       = 64
	defaultMaxContextBytes     = 64 * 1024
	defaultHistoryKeepLatest   = 256
	defaultHistoryMaxAge       = 30 * 24 * time.Hour
	defaultLLMConcurrency      = 4
	defaultRegistryMaxLive     = 256
	defaultMaxOutputTokens     = 1024
	defaultMaxResponseBytes    = 16 * 1024
	defaultProviderID          = "openai-compatible"
	defaultPolicyID            = "part1-chat-gate.v1"
	defaultWhatsAppEnabled     = true
	defaultPairingOutput       = "terminal"
	maxShutdownTimeout         = 5 * time.Minute
	maxResponseBytes           = 16 * 1024
)

type LookupEnv func(string) (string, bool)

type Snapshot struct {
	dataDir             string
	httpAddress         string
	logLevel            string
	logFormat           string
	shutdownTimeout     time.Duration
	whatsAppEnabled     bool
	agentEnabled        bool
	tenantID            identity.TenantID
	accountID           identity.AccountID
	ownerAddress        string
	allowlist           []string
	llmEndpoint         string
	llmAPIKey           string
	llmFallbackEndpoint string
	llmFallbackAPIKey   string
	langsmithAPIKey     string
	llmModel            string
	llmProviderID       identity.ProviderID
	llmTimeout          time.Duration
	llmConcurrency      uint32
	maxOutputTokens     uint32
	maxResponseBytes    uint32
	basePrompt          string
	policyID            identity.PolicyID
	policyRevision      uint64
	inboundQueue        uint32
	inboundWorkers      uint32
	commandQueue        uint32
	commandWorkers      uint32
	aiQueue             uint32
	aiWorkers           uint32
	messageDebounce     time.Duration
	messageBurstCap     uint32
	historyWindow       uint32
	maxContextBytes     uint32
	historyKeepLatest   uint32
	historyMaxAge       time.Duration
	registryMaxLive     uint32
	registryIdleTTL     time.Duration
	constructionTimeout time.Duration
	connectTimeout      time.Duration
	sendTimeout         time.Duration
	pairingOutput       string
	assistantName       string
}

// LoadRuntime loads configuration from the process environment and an optional
// dotenv file. Values explicitly present in the process environment take
// precedence over values from the file.
func LoadRuntime(lookup LookupEnv) (Snapshot, error) {
	mergedLookup, err := lookupWithDotEnv(lookup)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot, err := load(mergedLookup, false)
	if err != nil {
		return Snapshot{}, err
	}
	if !snapshot.whatsAppEnabled {
		return snapshot, nil
	}
	tenantID, accountID, err := resolveRuntimeIdentity(snapshot.dataDir)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot.tenantID = tenantID
	snapshot.accountID = accountID
	return snapshot, nil
}

// LoadDataDirRuntime resolves only the offline data-root setting. Backup must
// remain usable when an unrelated live-runtime setting (for example an LLM
// credential or endpoint) is missing or invalid.
func LoadDataDirRuntime(lookup LookupEnv) (string, error) {
	mergedLookup, err := lookupWithDotEnv(lookup)
	if err != nil {
		return "", err
	}
	dataDir, err := resolveDataDir(valueOrDefault(mergedLookup, "WAZZAP_DATA_DIR", defaultDataDir))
	if err != nil {
		return "", fmt.Errorf("WAZZAP_DATA_DIR: %w", err)
	}
	return dataDir, nil
}

func Load(lookup LookupEnv) (Snapshot, error) {
	return load(lookup, true)
}

func load(lookup LookupEnv, requireConfiguredIdentity bool) (Snapshot, error) {
	if lookup == nil {
		return Snapshot{}, errors.New("environment lookup is required")
	}
	dataDir, err := resolveDataDir(valueOrDefault(lookup, "WAZZAP_DATA_DIR", defaultDataDir))
	if err != nil {
		return Snapshot{}, fmt.Errorf("WAZZAP_DATA_DIR: %w", err)
	}
	httpAddress := valueOrDefault(lookup, "WAZZAP_HTTP_ADDRESS", defaultHTTPAddress)
	if err := validateHTTPAddress(httpAddress); err != nil {
		return Snapshot{}, fmt.Errorf("WAZZAP_HTTP_ADDRESS: %w", err)
	}
	logLevel := strings.ToLower(valueOrDefault(lookup, "WAZZAP_LOG_LEVEL", defaultLogLevel))
	if !oneOf(logLevel, "debug", "info", "warn", "error") {
		return Snapshot{}, fmt.Errorf("WAZZAP_LOG_LEVEL: unsupported value %q", logLevel)
	}
	logFormat := strings.ToLower(valueOrDefault(lookup, "WAZZAP_LOG_FORMAT", defaultLogFormat))
	if !oneOf(logFormat, "json", "text", "compact") {
		return Snapshot{}, fmt.Errorf("WAZZAP_LOG_FORMAT: unsupported value %q", logFormat)
	}
	shutdownTimeout, err := parseDuration(lookup, "WAZZAP_SHUTDOWN_TIMEOUT", defaultShutdownTimeout, maxShutdownTimeout)
	if err != nil {
		return Snapshot{}, err
	}
	whatsAppEnabled, err := parseBool(lookup, "WAZZAP_WHATSAPP_ENABLED", defaultWhatsAppEnabled)
	if err != nil {
		return Snapshot{}, err
	}
	agentEnabled, err := parseBool(lookup, "WAZZAP_AGENT_ENABLED", whatsAppEnabled)
	if err != nil {
		return Snapshot{}, err
	}
	if agentEnabled && !whatsAppEnabled {
		return Snapshot{}, fmt.Errorf("WAZZAP_WHATSAPP_ENABLED: must be true when WAZZAP_AGENT_ENABLED=true")
	}

	providerID, err := identity.ParseProviderID(valueOrDefault(lookup, "WAZZAP_LLM_PROVIDER_ID", defaultProviderID))
	if err != nil {
		return Snapshot{}, fmt.Errorf("WAZZAP_LLM_PROVIDER_ID: %w", err)
	}
	policyID, err := identity.ParsePolicyID(valueOrDefault(lookup, "WAZZAP_POLICY_ID", defaultPolicyID))
	if err != nil {
		return Snapshot{}, fmt.Errorf("WAZZAP_POLICY_ID: %w", err)
	}
	policyRevision, err := parseUint(lookup, "WAZZAP_POLICY_REVISION", 1, 1, ^uint64(0))
	if err != nil {
		return Snapshot{}, err
	}
	llmTimeout, err := parseDuration(lookup, "WAZZAP_LLM_TIMEOUT", defaultLLMTimeout, 10*time.Minute)
	if err != nil {
		return Snapshot{}, err
	}
	connectTimeout, err := parseDuration(lookup, "WAZZAP_CONNECT_TIMEOUT", defaultConnectTimeout, 10*time.Minute)
	if err != nil {
		return Snapshot{}, err
	}
	sendTimeout, err := parseDuration(lookup, "WAZZAP_SEND_TIMEOUT", defaultSendTimeout, 5*time.Minute)
	if err != nil {
		return Snapshot{}, err
	}
	registryIdleTTL, err := parseDuration(lookup, "WAZZAP_AGENT_IDLE_TTL", defaultRegistryIdleTTL, 24*time.Hour)
	if err != nil {
		return Snapshot{}, err
	}
	constructionTimeout, err := parseDuration(lookup, "WAZZAP_AGENT_CONSTRUCTION_TIMEOUT", defaultConstructionTimeout, time.Minute)
	if err != nil {
		return Snapshot{}, err
	}
	inboundQueue, err := parseUint(lookup, "WAZZAP_INBOUND_QUEUE", defaultInboundQueue, 1, 65_536)
	if err != nil {
		return Snapshot{}, err
	}
	inboundWorkers, err := parseUint(lookup, "WAZZAP_INBOUND_WORKERS", defaultInboundWorkers, 1, 256)
	if err != nil {
		return Snapshot{}, err
	}
	commandQueue, err := parseUint(lookup, "WAZZAP_COMMAND_QUEUE", defaultCommandQueue, 1, 65_536)
	if err != nil {
		return Snapshot{}, err
	}
	commandWorkers, err := parseUint(lookup, "WAZZAP_COMMAND_WORKERS", defaultCommandWorkers, 1, 256)
	if err != nil {
		return Snapshot{}, err
	}
	aiQueue, err := parseUint(lookup, "WAZZAP_AI_QUEUE", defaultAIQueue, 1, 65_536)
	if err != nil {
		return Snapshot{}, err
	}
	aiWorkers, err := parseUint(lookup, "WAZZAP_AI_WORKERS", defaultAIWorkers, 1, 256)
	if err != nil {
		return Snapshot{}, err
	}
	messageDebounce, err := parseDuration(lookup, "WAZZAP_MESSAGE_DEBOUNCE", defaultMessageDebounce, time.Minute)
	if err != nil {
		return Snapshot{}, err
	}
	messageBurstCap, err := parseUint(lookup, "WAZZAP_MESSAGE_BURST_CAP", defaultMessageBurstCap, 1, 256)
	if err != nil {
		return Snapshot{}, err
	}
	historyWindow, err := parseUint(lookup, "WAZZAP_HISTORY_WINDOW", defaultHistoryWindow, 1, 256)
	if err != nil {
		return Snapshot{}, err
	}
	maxContextBytes, err := parseUint(lookup, "WAZZAP_MAX_CONTEXT_BYTES", defaultMaxContextBytes, 1, 1024*1024)
	if err != nil {
		return Snapshot{}, err
	}
	historyKeepLatest, err := parseUint(lookup, "WAZZAP_HISTORY_KEEP_LATEST", defaultHistoryKeepLatest, 1, 1_000_000)
	if err != nil {
		return Snapshot{}, err
	}
	historyMaxAge, err := parseDuration(lookup, "WAZZAP_HISTORY_MAX_AGE", defaultHistoryMaxAge, 10*365*24*time.Hour)
	if err != nil {
		return Snapshot{}, err
	}
	llmConcurrency, err := parseUint(lookup, "WAZZAP_LLM_CONCURRENCY", defaultLLMConcurrency, 1, 256)
	if err != nil {
		return Snapshot{}, err
	}
	registryMaxLive, err := parseUint(lookup, "WAZZAP_AGENT_MAX_LIVE", defaultRegistryMaxLive, 1, 1_000_000)
	if err != nil {
		return Snapshot{}, err
	}
	maxOutputTokens, err := parseUint(lookup, "WAZZAP_MAX_OUTPUT_TOKENS", defaultMaxOutputTokens, 1, 65_536)
	if err != nil {
		return Snapshot{}, err
	}
	responseBytes, err := parseUint(lookup, "WAZZAP_MAX_RESPONSE_BYTES", defaultMaxResponseBytes, 1, maxResponseBytes)
	if err != nil {
		return Snapshot{}, err
	}
	pairingOutput := strings.ToLower(valueOrDefault(lookup, "WAZZAP_PAIRING_OUTPUT", defaultPairingOutput))
	if !oneOf(pairingOutput, "disabled", "terminal") {
		return Snapshot{}, fmt.Errorf("WAZZAP_PAIRING_OUTPUT: must be disabled or terminal")
	}

	snapshot := Snapshot{
		dataDir:             dataDir,
		httpAddress:         httpAddress,
		logLevel:            logLevel,
		logFormat:           logFormat,
		shutdownTimeout:     shutdownTimeout,
		whatsAppEnabled:     whatsAppEnabled,
		agentEnabled:        agentEnabled,
		llmAPIKey:           value(lookup, "WAZZAP_LLM_API_KEY"),
		llmEndpoint:         value(lookup, "WAZZAP_LLM_ENDPOINT"),
		llmFallbackEndpoint: value(lookup, "WAZZAP_LLM_FALLBACK_ENDPOINT"),
		llmFallbackAPIKey:   value(lookup, "WAZZAP_LLM_FALLBACK_API_KEY"),
		langsmithAPIKey:     value(lookup, "LANGSMITH_API_KEY"),
		llmModel:            value(lookup, "WAZZAP_LLM_MODEL"),
		llmProviderID:       providerID,
		llmTimeout:          llmTimeout,
		llmConcurrency:      uint32(llmConcurrency),
		maxOutputTokens:     uint32(maxOutputTokens),
		maxResponseBytes:    uint32(responseBytes),
		basePrompt:          valueOrDefault(lookup, "WAZZAP_BASE_PROMPT", ""),
		policyID:            policyID,
		policyRevision:      policyRevision,
		inboundQueue:        uint32(inboundQueue),
		inboundWorkers:      uint32(inboundWorkers),
		commandQueue:        uint32(commandQueue),
		commandWorkers:      uint32(commandWorkers),
		aiQueue:             uint32(aiQueue),
		aiWorkers:           uint32(aiWorkers),
		messageDebounce:     messageDebounce,
		messageBurstCap:     uint32(messageBurstCap),
		historyWindow:       uint32(historyWindow),
		maxContextBytes:     uint32(maxContextBytes),
		historyKeepLatest:   uint32(historyKeepLatest),
		historyMaxAge:       historyMaxAge,
		registryMaxLive:     uint32(registryMaxLive),
		registryIdleTTL:     registryIdleTTL,
		constructionTimeout: constructionTimeout,
		connectTimeout:      connectTimeout,
		sendTimeout:         sendTimeout,
		pairingOutput:       pairingOutput,
		ownerAddress:        value(lookup, "WAZZAP_OWNER_JID"),
		allowlist:           splitList(value(lookup, "WAZZAP_CHAT_ALLOWLIST")),
		assistantName:       value(lookup, "ASSISTANT_NAME"),
	}
	if whatsAppEnabled {
		if err := snapshot.validateEnabled(); err != nil {
			return Snapshot{}, err
		}
		if requireConfiguredIdentity {
			if err := snapshot.loadRequiredIdentity(lookup); err != nil {
				return Snapshot{}, err
			}
		}
	}
	return snapshot, nil
}

func (snapshot *Snapshot) loadRequiredIdentity(lookup LookupEnv) error {
	tenantID, err := identity.ParseTenantID(value(lookup, "WAZZAP_TENANT_ID"))
	if err != nil {
		return fmt.Errorf("WAZZAP_TENANT_ID: %w", err)
	}
	accountID, err := identity.ParseAccountID(value(lookup, "WAZZAP_ACCOUNT_ID"))
	if err != nil {
		return fmt.Errorf("WAZZAP_ACCOUNT_ID: %w", err)
	}
	snapshot.tenantID = tenantID
	snapshot.accountID = accountID
	return nil
}

func (snapshot Snapshot) validateEnabled() error {
	required := []struct {
		key   string
		value string
	}{
		{"WAZZAP_OWNER_JID", snapshot.ownerAddress},
		{"WAZZAP_LLM_ENDPOINT", snapshot.llmEndpoint},
		{"WAZZAP_LLM_API_KEY", snapshot.llmAPIKey},
		{"WAZZAP_LLM_MODEL", snapshot.llmModel},
	}
	for _, field := range required {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("%s: required when WAZZAP_WHATSAPP_ENABLED=true", field.key)
		}
	}
	if len(snapshot.allowlist) == 0 {
		return fmt.Errorf("WAZZAP_CHAT_ALLOWLIST: at least one target is required when WAZZAP_WHATSAPP_ENABLED=true")
	}
	if len(snapshot.allowlist) > 1024 {
		return fmt.Errorf("WAZZAP_CHAT_ALLOWLIST: at most 1024 targets are allowed")
	}
	for _, address := range append(append([]string(nil), snapshot.allowlist...), snapshot.ownerAddress) {
		if err := validateOpaqueAddress(address); err != nil {
			return fmt.Errorf("provider address: %w", err)
		}
	}
	parsedEndpoint, err := url.Parse(snapshot.llmEndpoint)
	if err != nil || parsedEndpoint.Scheme == "" || parsedEndpoint.Host == "" {
		return fmt.Errorf("WAZZAP_LLM_ENDPOINT: must be an absolute HTTP(S) URL")
	}
	if parsedEndpoint.Scheme != "http" && parsedEndpoint.Scheme != "https" {
		return fmt.Errorf("WAZZAP_LLM_ENDPOINT: must use HTTP or HTTPS")
	}
	if (snapshot.llmFallbackEndpoint == "") != (snapshot.llmFallbackAPIKey == "") {
		return fmt.Errorf("WAZZAP_LLM_FALLBACK_ENDPOINT and WAZZAP_LLM_FALLBACK_API_KEY: must be configured together")
	}
	if snapshot.llmFallbackEndpoint != "" {
		fallbackEndpoint, parseErr := url.Parse(snapshot.llmFallbackEndpoint)
		if parseErr != nil || fallbackEndpoint.Scheme == "" || fallbackEndpoint.Host == "" ||
			(fallbackEndpoint.Scheme != "http" && fallbackEndpoint.Scheme != "https") {
			return fmt.Errorf("WAZZAP_LLM_FALLBACK_ENDPOINT: must be an absolute HTTP(S) URL")
		}
	}
	if strings.TrimSpace(snapshot.basePrompt) == "" || len(snapshot.basePrompt) > 16*1024 {
		return fmt.Errorf("WAZZAP_BASE_PROMPT: must be non-empty and at most 16384 bytes")
	}
	return nil
}

func (snapshot Snapshot) DataDir() string                    { return snapshot.dataDir }
func (snapshot Snapshot) HTTPAddress() string                { return snapshot.httpAddress }
func (snapshot Snapshot) LogLevel() string                   { return snapshot.logLevel }
func (snapshot Snapshot) LogFormat() string                  { return snapshot.logFormat }
func (snapshot Snapshot) ShutdownTimeout() time.Duration     { return snapshot.shutdownTimeout }
func (snapshot Snapshot) WhatsAppEnabled() bool              { return snapshot.whatsAppEnabled }
func (snapshot Snapshot) AgentEnabled() bool                 { return snapshot.agentEnabled }
func (snapshot Snapshot) TenantID() identity.TenantID        { return snapshot.tenantID }
func (snapshot Snapshot) AccountID() identity.AccountID      { return snapshot.accountID }
func (snapshot Snapshot) OwnerAddress() string               { return snapshot.ownerAddress }
func (snapshot Snapshot) LLMEndpoint() string                { return snapshot.llmEndpoint }
func (snapshot Snapshot) LLMAPIKey() string                  { return snapshot.llmAPIKey }
func (snapshot Snapshot) LLMFallbackEndpoint() string        { return snapshot.llmFallbackEndpoint }
func (snapshot Snapshot) LLMFallbackAPIKey() string          { return snapshot.llmFallbackAPIKey }
func (snapshot Snapshot) LangSmithAPIKey() string            { return snapshot.langsmithAPIKey }
func (snapshot Snapshot) LLMModel() string                   { return snapshot.llmModel }
func (snapshot Snapshot) LLMProviderID() identity.ProviderID { return snapshot.llmProviderID }
func (snapshot Snapshot) LLMTimeout() time.Duration          { return snapshot.llmTimeout }
func (snapshot Snapshot) LLMConcurrency() uint32             { return snapshot.llmConcurrency }
func (snapshot Snapshot) MaxOutputTokens() uint32            { return snapshot.maxOutputTokens }
func (snapshot Snapshot) MaxResponseBytes() uint32           { return snapshot.maxResponseBytes }
func (snapshot Snapshot) BasePrompt() string                 { return snapshot.basePrompt }
func (snapshot Snapshot) PolicyID() identity.PolicyID        { return snapshot.policyID }
func (snapshot Snapshot) PolicyRevision() uint64             { return snapshot.policyRevision }
func (snapshot Snapshot) InboundQueue() uint32               { return snapshot.inboundQueue }
func (snapshot Snapshot) InboundWorkers() uint32             { return snapshot.inboundWorkers }
func (snapshot Snapshot) CommandQueue() uint32               { return snapshot.commandQueue }
func (snapshot Snapshot) CommandWorkers() uint32             { return snapshot.commandWorkers }
func (snapshot Snapshot) AIQueue() uint32                    { return snapshot.aiQueue }
func (snapshot Snapshot) AIWorkers() uint32                  { return snapshot.aiWorkers }
func (snapshot Snapshot) MessageDebounce() time.Duration     { return snapshot.messageDebounce }
func (snapshot Snapshot) MessageBurstCap() uint32            { return snapshot.messageBurstCap }
func (snapshot Snapshot) HistoryWindow() uint32              { return snapshot.historyWindow }
func (snapshot Snapshot) MaxContextBytes() uint32            { return snapshot.maxContextBytes }
func (snapshot Snapshot) HistoryKeepLatest() uint32          { return snapshot.historyKeepLatest }
func (snapshot Snapshot) HistoryMaxAge() time.Duration       { return snapshot.historyMaxAge }
func (snapshot Snapshot) RegistryMaxLive() uint32            { return snapshot.registryMaxLive }
func (snapshot Snapshot) RegistryIdleTTL() time.Duration     { return snapshot.registryIdleTTL }
func (snapshot Snapshot) ConstructionTimeout() time.Duration { return snapshot.constructionTimeout }
func (snapshot Snapshot) ConnectTimeout() time.Duration      { return snapshot.connectTimeout }
func (snapshot Snapshot) SendTimeout() time.Duration         { return snapshot.sendTimeout }
func (snapshot Snapshot) PairingOutput() string              { return snapshot.pairingOutput }
func (snapshot Snapshot) AssistantName() string              { return snapshot.assistantName }

func (snapshot Snapshot) Allowlist() []string {
	return append([]string(nil), snapshot.allowlist...)
}

func (snapshot Snapshot) TenantDataDir() string {
	if snapshot.tenantID.IsZero() {
		return snapshot.dataDir
	}
	return filepath.Join(snapshot.dataDir, "tenants", snapshot.tenantID.String())
}

func (snapshot Snapshot) AppDatabasePath() string {
	return filepath.Join(snapshot.TenantDataDir(), "app.db")
}
func (snapshot Snapshot) WhatsAppDatabasePath() string {
	return filepath.Join(snapshot.TenantDataDir(), "whatsapp.db")
}

func (snapshot Snapshot) Redacted() map[string]any {
	return map[string]any{
		"data_dir":                snapshot.dataDir,
		"http_address":            snapshot.httpAddress,
		"log_level":               snapshot.logLevel,
		"log_format":              snapshot.logFormat,
		"shutdown_timeout":        snapshot.shutdownTimeout.String(),
		"whatsapp_enabled":        snapshot.whatsAppEnabled,
		"agent_enabled":           snapshot.agentEnabled,
		"tenant_configured":       !snapshot.tenantID.IsZero(),
		"account_configured":      !snapshot.accountID.IsZero(),
		"owner_configured":        snapshot.ownerAddress != "",
		"allowlist_count":         len(snapshot.allowlist),
		"llm_endpoint_configured": snapshot.llmEndpoint != "",
		"llm_api_key_configured":  snapshot.llmAPIKey != "",
		"llm_fallback_configured": snapshot.llmFallbackEndpoint != "",
		"langsmith_configured":    snapshot.langsmithAPIKey != "",
		"llm_model_configured":    snapshot.llmModel != "",
		"llm_concurrency":         snapshot.llmConcurrency,
		"max_response_bytes":      snapshot.maxResponseBytes,
		"inbound_queue":           snapshot.inboundQueue,
		"inbound_workers":         snapshot.inboundWorkers,
		"command_queue":           snapshot.commandQueue,
		"command_workers":         snapshot.commandWorkers,
		"ai_queue":                snapshot.aiQueue,
		"ai_workers":              snapshot.aiWorkers,
		"message_debounce":        snapshot.messageDebounce.String(),
		"message_burst_cap":       snapshot.messageBurstCap,
		"history_window":          snapshot.historyWindow,
		"max_context_bytes":       snapshot.maxContextBytes,
		"history_keep_latest":     snapshot.historyKeepLatest,
		"history_max_age":         snapshot.historyMaxAge.String(),
		"pairing_output":          snapshot.pairingOutput,
	}
}

func value(lookup LookupEnv, key string) string {
	value, _ := lookup(key)
	return strings.TrimSpace(value)
}

func valueOrDefault(lookup LookupEnv, key, fallback string) string {
	if value := value(lookup, key); value != "" {
		return value
	}
	return fallback
}

func resolveDataDir(value string) (string, error) {
	if strings.ContainsRune(value, '\x00') {
		return "", errors.New("must not contain a null byte")
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	if filepath.Dir(absolute) == absolute {
		return "", errors.New("filesystem root is not allowed")
	}
	return absolute, nil
}

func validateHTTPAddress(value string) error {
	host, portText, err := net.SplitHostPort(value)
	if err != nil {
		return err
	}
	if strings.ContainsRune(host, '\x00') {
		return errors.New("host must not contain a null byte")
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		return fmt.Errorf("invalid port: %w", err)
	}
	if port > 65535 {
		return errors.New("port must be between 0 and 65535")
	}
	return nil
}

func parseDuration(lookup LookupEnv, key string, fallback, maximum time.Duration) (time.Duration, error) {
	value := value(lookup, key)
	if value == "" {
		return fallback, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	if duration <= 0 || duration > maximum {
		return 0, fmt.Errorf("%s: must be greater than zero and at most %s", key, maximum)
	}
	return duration, nil
}

func parseBool(lookup LookupEnv, key string, fallback bool) (bool, error) {
	value := value(lookup, key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s: must be true or false", key)
	}
	return parsed, nil
}

func parseUint(lookup LookupEnv, key string, fallback, minimum, maximum uint64) (uint64, error) {
	value := value(lookup, key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil || parsed < minimum || parsed > maximum {
		return 0, fmt.Errorf("%s: must be between %d and %d", key, minimum, maximum)
	}
	return parsed, nil
}

func splitList(value string) []string {
	if value == "" {
		return nil
	}
	seen := make(map[string]struct{})
	result := make([]string, 0)
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, exists := seen[item]; exists {
			continue
		}
		seen[item] = struct{}{}
		result = append(result, item)
	}
	return result
}

func validateOpaqueAddress(value string) error {
	if value == "" || len(value) > 512 {
		return fmt.Errorf("must be non-empty and at most 512 bytes")
	}
	for _, character := range value {
		if unicode.IsSpace(character) || unicode.IsControl(character) {
			return fmt.Errorf("must not contain whitespace or control characters")
		}
	}
	return nil
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}
