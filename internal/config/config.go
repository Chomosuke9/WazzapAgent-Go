package config

import (
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

const (
	defaultDataDir           = "./data"
	defaultHTTPAddress       = "127.0.0.1:8080"
	defaultLogLevel          = "info"
	defaultLogFormat         = "compact"
	defaultShutdownTimeout   = 30 * time.Second
	defaultLLMTimeout        = 60 * time.Second
	defaultConnectTimeout    = 90 * time.Second
	defaultSendTimeout       = 30 * time.Second
	defaultInboundQueue      = 512
	defaultInboundWorkers    = 4
	defaultMessageDebounce   = 350 * time.Millisecond
	defaultMessageBurstCap   = 8
	defaultHistoryWindow     = 64
	defaultMaxContextBytes   = 64 * 1024
	defaultHistoryKeepLatest = 256
	defaultHistoryMaxAge     = 30 * 24 * time.Hour
	defaultLLMConcurrency    = 4
	defaultMaxOutputTokens   = 1024
	defaultMaxResponseBytes  = 16 * 1024
	defaultProviderID        = "openai-compatible"
	defaultPolicyID          = "part1-chat-gate.v1"
	defaultWhatsAppEnabled   = true
	defaultPairingOutput     = "terminal"
	maxShutdownTimeout       = 5 * time.Minute
	maxResponseBytes         = 16 * 1024
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
	messageDebounce     time.Duration
	messageBurstCap     uint32
	historyWindow       uint32
	maxContextBytes     uint32
	historyKeepLatest   uint32
	historyMaxAge       time.Duration
	connectTimeout      time.Duration
	sendTimeout         time.Duration
	pairingOutput       string
	assistantName       string
	chatDefaults        ChatDefaults
}

// LoadRuntime loads configuration from the process environment and an optional
// dotenv file. Values explicitly present in the process environment take
// precedence over values from the file.
func LoadRuntime(lookup LookupEnv) (Snapshot, error) {
	snapshot, err := LoadRuntimeBootstrap(lookup)
	if err != nil {
		return Snapshot{}, err
	}
	return snapshot.ResolveRuntimeIdentity()
}

// LoadRuntimeBootstrap parses the CLI environment and dotenv sources without
// touching the data root. Callers must acquire the platform data-root lease
// before resolving the durable runtime identity or opening any store.
func LoadRuntimeBootstrap(lookup LookupEnv) (Snapshot, error) {
	mergedLookup, err := lookupWithDotEnv(lookup)
	if err != nil {
		return Snapshot{}, err
	}
	return load(mergedLookup, false)
}

// ResolveRuntimeIdentity loads or creates the durable identity after the
// caller has acquired ownership of the data root.
func (snapshot Snapshot) ResolveRuntimeIdentity() (Snapshot, error) {
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
	settings, err := SettingsFromEnv(lookup)
	if err != nil {
		return Snapshot{}, err
	}
	// The environment-driven runtime always composes the agent, so its
	// settings are required whenever WhatsApp is on, even with the agent
	// switched off.
	if settings.WhatsAppEnabled {
		if err := ValidateAgent(settings); err != nil {
			return Snapshot{}, err
		}
	}
	snapshot, err := SnapshotFromSettings(settings)
	if err != nil {
		return Snapshot{}, err
	}
	if snapshot.whatsAppEnabled && requireConfiguredIdentity {
		if err := snapshot.loadRequiredIdentity(lookup); err != nil {
			return Snapshot{}, err
		}
	}
	return snapshot, nil
}

// SettingsFromEnv reads the same Settings the settings UI edits from
// environment variables, so both sources go through one set of defaults and
// validation. Unset values stay zero and take their defaults.
func SettingsFromEnv(lookup LookupEnv) (Settings, error) {
	if lookup == nil {
		return Settings{}, errors.New("environment lookup is required")
	}
	settings := Settings{
		AssistantName: value(lookup, "ASSISTANT_NAME"), BasePrompt: value(lookup, "WAZZAP_BASE_PROMPT"),
		ChatDefaults: DefaultChatDefaults(),
		OwnerJID:     value(lookup, "WAZZAP_OWNER_JID"), ChatAllowlist: splitList(value(lookup, "WAZZAP_CHAT_ALLOWLIST")),
		LLMEndpoint: value(lookup, "WAZZAP_LLM_ENDPOINT"), LLMAPIKey: value(lookup, "WAZZAP_LLM_API_KEY"),
		LLMModel: value(lookup, "WAZZAP_LLM_MODEL"), LLMProviderID: value(lookup, "WAZZAP_LLM_PROVIDER_ID"),
		FallbackEndpoint: value(lookup, "WAZZAP_LLM_FALLBACK_ENDPOINT"), FallbackAPIKey: value(lookup, "WAZZAP_LLM_FALLBACK_API_KEY"),
		PolicyID: value(lookup, "WAZZAP_POLICY_ID"), LogLevel: value(lookup, "WAZZAP_LOG_LEVEL"),
		LogFormat: value(lookup, "WAZZAP_LOG_FORMAT"), LangSmithAPIKey: value(lookup, "LANGSMITH_API_KEY"),
		DataDir: value(lookup, "WAZZAP_DATA_DIR"), HTTPAddress: value(lookup, "WAZZAP_HTTP_ADDRESS"),
		PairingOutput: value(lookup, "WAZZAP_PAIRING_OUTPUT"),
	}
	var err error
	if settings.WhatsAppEnabled, err = parseBool(lookup, "WAZZAP_WHATSAPP_ENABLED", defaultWhatsAppEnabled); err != nil {
		return Settings{}, err
	}
	if settings.AgentEnabled, err = parseBool(lookup, "WAZZAP_AGENT_ENABLED", settings.WhatsAppEnabled); err != nil {
		return Settings{}, err
	}
	if settings.PolicyRevision, err = parseUint(lookup, "WAZZAP_POLICY_REVISION", 0, 1, ^uint64(0)); err != nil {
		return Settings{}, err
	}
	// An explicit value must be positive; its upper bound is checked with the
	// rest of the settings.
	durations := []struct {
		key    string
		target *time.Duration
	}{
		{"WAZZAP_SHUTDOWN_TIMEOUT", &settings.ShutdownTimeout}, {"WAZZAP_LLM_TIMEOUT", &settings.LLMTimeout},
		{"WAZZAP_CONNECT_TIMEOUT", &settings.ConnectTimeout}, {"WAZZAP_SEND_TIMEOUT", &settings.SendTimeout},
		{"WAZZAP_MESSAGE_DEBOUNCE", &settings.MessageDebounce}, {"WAZZAP_HISTORY_MAX_AGE", &settings.HistoryMaxAge},
	}
	for _, field := range durations {
		if *field.target, err = parseDuration(lookup, field.key, 0, time.Duration(1<<63-1)); err != nil {
			return Settings{}, err
		}
	}
	numbers := []struct {
		key    string
		target *uint32
	}{
		{"WAZZAP_LLM_CONCURRENCY", &settings.LLMConcurrency}, {"WAZZAP_MAX_OUTPUT_TOKENS", &settings.MaxOutputTokens},
		{"WAZZAP_MAX_RESPONSE_BYTES", &settings.MaxResponseBytes}, {"WAZZAP_HISTORY_WINDOW", &settings.HistoryWindow},
		{"WAZZAP_MAX_CONTEXT_BYTES", &settings.MaxContextBytes}, {"WAZZAP_HISTORY_KEEP_LATEST", &settings.HistoryKeepLatest},
		{"WAZZAP_INBOUND_QUEUE", &settings.InboundQueue}, {"WAZZAP_INBOUND_WORKERS", &settings.InboundWorkers},
		{"WAZZAP_MESSAGE_BURST_CAP", &settings.MessageBurstCap},
	}
	for _, field := range numbers {
		parsed, err := parseUint(lookup, field.key, 0, 1, 1<<32-1)
		if err != nil {
			return Settings{}, err
		}
		*field.target = uint32(parsed)
	}
	return settings, nil
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
func (snapshot Snapshot) MessageDebounce() time.Duration     { return snapshot.messageDebounce }
func (snapshot Snapshot) MessageBurstCap() uint32            { return snapshot.messageBurstCap }
func (snapshot Snapshot) HistoryWindow() uint32              { return snapshot.historyWindow }
func (snapshot Snapshot) MaxContextBytes() uint32            { return snapshot.maxContextBytes }
func (snapshot Snapshot) HistoryKeepLatest() uint32          { return snapshot.historyKeepLatest }
func (snapshot Snapshot) HistoryMaxAge() time.Duration       { return snapshot.historyMaxAge }
func (snapshot Snapshot) ConnectTimeout() time.Duration      { return snapshot.connectTimeout }
func (snapshot Snapshot) SendTimeout() time.Duration         { return snapshot.sendTimeout }
func (snapshot Snapshot) PairingOutput() string              { return snapshot.pairingOutput }
func (snapshot Snapshot) AssistantName() string              { return snapshot.assistantName }
func (snapshot Snapshot) ChatDefaults() ChatDefaults         { return snapshot.chatDefaults }

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
