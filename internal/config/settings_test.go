package config

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

func TestChatDefaultsPersistAndReachRuntime(t *testing.T) {
	settings := DefaultSettings()
	settings.WhatsAppEnabled = false
	settings.AgentEnabled = false
	settings.ChatDefaults = ChatDefaults{ModerationLevel: 2, PromptMode: "replace", PromptText: "Use short replies", TriggerName: true, TriggerNameRegex: true, TriggerNamePattern: `(?i)vivy`}
	encoded, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	loaded := DefaultSettings()
	if err := json.Unmarshal(encoded, &loaded); err != nil {
		t.Fatal(err)
	}
	snapshot, err := SnapshotFromSettings(loaded)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ChatDefaults() != settings.ChatDefaults {
		t.Fatalf("chat defaults changed: %#v", snapshot.ChatDefaults())
	}
	legacy := DefaultSettings()
	if err := json.Unmarshal([]byte(`{"BasePrompt":"legacy"}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if !legacy.ChatDefaults.TriggerMention || !legacy.ChatDefaults.TriggerReply {
		t.Fatalf("legacy defaults changed: %#v", legacy.ChatDefaults)
	}
}

func TestChatDefaultsRejectInvalidValues(t *testing.T) {
	settings := DefaultSettings()
	settings.ChatDefaults.TriggerName = true
	settings.ChatDefaults.TriggerNameRegex = true
	settings.ChatDefaults.TriggerNamePattern = "["
	if err := ValidateDraft(settings); err == nil {
		t.Fatal("invalid default regex accepted")
	}
	settings.ChatDefaults = DefaultChatDefaults()
	settings.ChatDefaults.ModerationLevel = 4
	if err := ValidateDraft(settings); err == nil {
		t.Fatal("invalid default moderation accepted")
	}
}

func TestDefaultSettingsCanBeSavedBeforeAgentSetup(t *testing.T) {
	settings := DefaultSettings()
	if err := ValidateDraft(settings); err != nil {
		t.Fatalf("default draft rejected: %v", err)
	}
	if err := ValidateSession(settings); err != nil {
		t.Fatalf("default session rejected: %v", err)
	}
	if err := ValidateAgent(settings); err == nil {
		t.Fatal("incomplete default settings accepted as Agent-ready")
	}
	issues := AgentReadinessIssues(settings)
	if len(issues) == 0 || !hasIssueField(issues, "ASSISTANT_NAME") || !hasIssueField(issues, "WAZZAP_LLM_API_KEY") {
		t.Fatalf("Agent readiness issues = %#v", issues)
	}
}

func TestZeroValueSettingsUseNonSecretDefaults(t *testing.T) {
	if err := ValidateDraft(Settings{}); err != nil {
		t.Fatalf("zero-value draft rejected: %v", err)
	}
	settings := Settings{WhatsAppEnabled: false, AgentEnabled: false}
	snapshot, err := SnapshotFromSettings(settings)
	if err != nil {
		t.Fatalf("zero-value snapshot: %v", err)
	}
	if snapshot.DataDir() == "" || snapshot.LLMTimeout() != defaultLLMTimeout || snapshot.PolicyRevision() != 1 {
		t.Fatalf("zero-value defaults = %#v", snapshot.Redacted())
	}
}

func TestSettingsSnapshotValidAgentHasNoExternalReads(t *testing.T) {
	settings := DefaultSettings()
	settings.AssistantName = "Vivy"
	settings.BasePrompt = "Jawab singkat."
	settings.OwnerJID = "15550000001@s.whatsapp.net"
	settings.ChatAllowlist = []string{"15550000002@s.whatsapp.net"}
	settings.LLMEndpoint = "https://llm.example.invalid/v1/chat/completions"
	settings.LLMAPIKey = "test-secret"
	settings.LLMModel = "test-model"
	settings.LLMTimeout = 12 * time.Second
	settings.TenantID, settings.AccountID = newTestIdentity(t)

	snapshot, err := SnapshotFromSettings(settings)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.AssistantName() != "Vivy" || snapshot.LLMTimeout() != 12*time.Second || snapshot.LLMAPIKey() != "test-secret" {
		t.Fatalf("snapshot values = %#v", snapshot.Redacted())
	}
	if snapshot.TenantID().IsZero() || snapshot.AccountID().IsZero() {
		t.Fatal("snapshot lost bootstrap identity")
	}
	settings.ChatAllowlist[0] = "mutated"
	if snapshot.Allowlist()[0] == "mutated" {
		t.Fatal("snapshot retained mutable allowlist backing array")
	}
}

func TestSettingsPublicMasksAllSecrets(t *testing.T) {
	settings := DefaultSettings()
	settings.LLMAPIKey = "primary-secret"
	settings.FallbackAPIKey = "fallback-secret"
	settings.LangSmithAPIKey = "trace-secret"
	public := settings.Public()
	if !public.LLMAPIKeyConfigured || !public.FallbackAPIKeyConfigured || !public.LangSmithAPIKeyConfigured {
		t.Fatalf("secret status = %#v", public)
	}
	for _, value := range []string{public.LLMAPIKey, public.FallbackAPIKey, public.LangSmithAPIKey} {
		if value != "" {
			t.Fatalf("public settings exposed secret value %q", value)
		}
	}
}

func TestSettingsRejectMalformedValuesWithoutLeakingSecrets(t *testing.T) {
	settings := DefaultSettings()
	settings.LogLevel = "trace"
	settings.LLMAPIKey = "must-not-leak"
	err := ValidateDraft(settings)
	if err == nil || !strings.Contains(err.Error(), "WAZZAP_LOG_LEVEL") || strings.Contains(err.Error(), settings.LLMAPIKey) {
		t.Fatalf("validation error = %v", err)
	}

	settings = DefaultSettings()
	settings.WhatsAppEnabled = false
	settings.AgentEnabled = true
	if err := ValidateDraft(settings); err == nil || !strings.Contains(err.Error(), "WAZZAP_AGENT_ENABLED") {
		t.Fatalf("invalid mode error = %v", err)
	}
}

func TestSettingsSchemaContainsAllPersistedAndRuntimeKeys(t *testing.T) {
	want := []string{
		"ASSISTANT_NAME", "WAZZAP_BASE_PROMPT", "WAZZAP_WHATSAPP_ENABLED", "WAZZAP_AGENT_ENABLED",
		"WAZZAP_OWNER_JID", "WAZZAP_CHAT_ALLOWLIST", "WAZZAP_LLM_ENDPOINT", "WAZZAP_LLM_API_KEY",
		"WAZZAP_LLM_MODEL", "WAZZAP_LLM_PROVIDER_ID", "WAZZAP_LLM_FALLBACK_ENDPOINT", "WAZZAP_LLM_FALLBACK_API_KEY",
		"WAZZAP_LLM_TIMEOUT", "WAZZAP_LLM_CONCURRENCY", "WAZZAP_MAX_OUTPUT_TOKENS", "WAZZAP_MAX_RESPONSE_BYTES",
		"WAZZAP_HISTORY_WINDOW", "WAZZAP_MAX_CONTEXT_BYTES", "WAZZAP_HISTORY_KEEP_LATEST", "WAZZAP_HISTORY_MAX_AGE",
		"WAZZAP_INBOUND_QUEUE", "WAZZAP_INBOUND_WORKERS", "WAZZAP_COMMAND_QUEUE", "WAZZAP_COMMAND_WORKERS",
		"WAZZAP_AI_QUEUE", "WAZZAP_AI_WORKERS", "WAZZAP_MESSAGE_DEBOUNCE", "WAZZAP_MESSAGE_BURST_CAP",
		"WAZZAP_AGENT_MAX_LIVE", "WAZZAP_AGENT_IDLE_TTL", "WAZZAP_AGENT_CONSTRUCTION_TIMEOUT", "WAZZAP_CONNECT_TIMEOUT",
		"WAZZAP_SEND_TIMEOUT", "WAZZAP_SHUTDOWN_TIMEOUT", "WAZZAP_POLICY_ID", "WAZZAP_POLICY_REVISION",
		"WAZZAP_LOG_LEVEL", "WAZZAP_LOG_FORMAT", "LANGSMITH_API_KEY", "WAZZAP_DATA_DIR", "WAZZAP_ENV_FILE",
		"WAZZAP_HTTP_ADDRESS", "WAZZAP_PAIRING_OUTPUT", "WAZZAP_TENANT_ID", "WAZZAP_ACCOUNT_ID", "NO_COLOR", "FORCE_COLOR", "startOnLaunch",
	}
	fields := SettingsSchema()
	seen := make(map[string]bool, len(fields))
	for _, field := range fields {
		if seen[field.Key] {
			t.Fatalf("duplicate schema key %q", field.Key)
		}
		seen[field.Key] = true
	}
	for _, key := range want {
		if !seen[key] {
			t.Fatalf("schema missing %q", key)
		}
	}
}

func hasIssueField(issues []ReadinessIssue, field string) bool {
	for _, issue := range issues {
		if issue.Field == field {
			return true
		}
	}
	return false
}

func newTestIdentity(t *testing.T) (tenant identity.TenantID, account identity.AccountID) {
	t.Helper()
	var err error
	tenant, err = identity.NewTenantID()
	if err != nil {
		t.Fatal(err)
	}
	account, err = identity.NewAccountID()
	if err != nil {
		t.Fatal(err)
	}
	return tenant, account
}
