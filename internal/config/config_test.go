package config

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("load defaults: %v", err)
	}
	if !filepath.IsAbs(cfg.DataDir()) {
		t.Fatalf("data directory is not absolute: %q", cfg.DataDir())
	}
	if cfg.HTTPAddress() != defaultHTTPAddress {
		t.Fatalf("HTTP address = %q, want %q", cfg.HTTPAddress(), defaultHTTPAddress)
	}
	if cfg.LogLevel() != defaultLogLevel || cfg.LogFormat() != defaultLogFormat {
		t.Fatalf("log defaults = %q/%q", cfg.LogLevel(), cfg.LogFormat())
	}
	if cfg.ShutdownTimeout() != defaultShutdownTimeout {
		t.Fatalf("shutdown timeout = %s, want %s", cfg.ShutdownTimeout(), defaultShutdownTimeout)
	}
	if cfg.MaxResponseBytes() != defaultMaxResponseBytes {
		t.Fatalf("response byte limit = %d, want %d", cfg.MaxResponseBytes(), defaultMaxResponseBytes)
	}
}

func TestAgentModeFailsClosedAndLoadsValidatedPartOneConfig(t *testing.T) {
	if _, err := Load(mapLookup(map[string]string{"WAZZAP_AGENT_ENABLED": "true"})); err == nil {
		t.Fatal("enabled mode accepted missing identity, owner, model, and allowlist")
	}
	tenantID, _ := identity.NewTenantID()
	accountID, _ := identity.NewAccountID()
	values := map[string]string{
		"WAZZAP_WHATSAPP_ENABLED": "true",
		"WAZZAP_AGENT_ENABLED":    "true",
		"WAZZAP_TENANT_ID":        tenantID.String(),
		"WAZZAP_ACCOUNT_ID":       accountID.String(),
		"WAZZAP_OWNER_JID":        "15550000001@s.whatsapp.net",
		"WAZZAP_CHAT_ALLOWLIST":   "15550000002@s.whatsapp.net,120363000000000001@g.us",
		"WAZZAP_LLM_ENDPOINT":     "https://llm.example.invalid/v1/chat/completions",
		"WAZZAP_LLM_API_KEY":      "very-secret-key",
		"WAZZAP_LLM_MODEL":        "test-model",
		"WAZZAP_BASE_PROMPT":      "private base prompt",
	}
	cfg, err := Load(mapLookup(values))
	if err != nil {
		t.Fatalf("load enabled config: %v", err)
	}
	if !cfg.AgentEnabled() || cfg.TenantID() != tenantID || cfg.AccountID() != accountID || len(cfg.Allowlist()) != 2 {
		t.Fatalf("enabled config mismatch: %#v", cfg.Redacted())
	}
	copyAllowlist := cfg.Allowlist()
	copyAllowlist[0] = "mutated"
	if cfg.Allowlist()[0] == "mutated" {
		t.Fatal("allowlist accessor did not return a defensive copy")
	}
	redactedJSON, _ := json.Marshal(cfg.Redacted())
	redacted := string(redactedJSON)
	for _, sensitive := range []string{values["WAZZAP_LLM_API_KEY"], values["WAZZAP_OWNER_JID"], "15550000002", values["WAZZAP_BASE_PROMPT"]} {
		if strings.Contains(redacted, sensitive) {
			t.Fatalf("redacted config contains sensitive value %q: %s", sensitive, redacted)
		}
	}
}

func TestAgentModeRejectsEmptyOrMalformedAllowlist(t *testing.T) {
	tenantID, _ := identity.NewTenantID()
	accountID, _ := identity.NewAccountID()
	base := map[string]string{
		"WAZZAP_WHATSAPP_ENABLED": "true",
		"WAZZAP_AGENT_ENABLED":    "true",
		"WAZZAP_TENANT_ID":        tenantID.String(),
		"WAZZAP_ACCOUNT_ID":       accountID.String(),
		"WAZZAP_OWNER_JID":        "15550000001@s.whatsapp.net",
		"WAZZAP_LLM_ENDPOINT":     "https://example.invalid/chat/completions",
		"WAZZAP_LLM_API_KEY":      "secret",
		"WAZZAP_LLM_MODEL":        "model",
	}
	if _, err := Load(mapLookup(base)); err == nil {
		t.Fatal("enabled mode accepted an empty allowlist")
	}
	base["WAZZAP_CHAT_ALLOWLIST"] = "bad address with spaces"
	if _, err := Load(mapLookup(base)); err == nil {
		t.Fatal("enabled mode accepted malformed provider address")
	}
}

func TestWhatsAppRuntimeCanPairWhileAgentKillSwitchIsOff(t *testing.T) {
	tenantID, _ := identity.NewTenantID()
	accountID, _ := identity.NewAccountID()
	cfg, err := Load(mapLookup(map[string]string{
		"WAZZAP_WHATSAPP_ENABLED": "true",
		"WAZZAP_AGENT_ENABLED":    "false",
		"WAZZAP_TENANT_ID":        tenantID.String(),
		"WAZZAP_ACCOUNT_ID":       accountID.String(),
		"WAZZAP_OWNER_JID":        "15550000001@s.whatsapp.net",
		"WAZZAP_CHAT_ALLOWLIST":   "15550000002@s.whatsapp.net",
		"WAZZAP_LLM_ENDPOINT":     "https://llm.example.invalid/v1/chat/completions",
		"WAZZAP_LLM_API_KEY":      "secret",
		"WAZZAP_LLM_MODEL":        "test-model",
		"WAZZAP_PAIRING_OUTPUT":   "terminal",
	}))
	if err != nil {
		t.Fatalf("load pairing-only runtime: %v", err)
	}
	if !cfg.WhatsAppEnabled() || cfg.AgentEnabled() {
		t.Fatalf("runtime/kill-switch state = %v/%v", cfg.WhatsAppEnabled(), cfg.AgentEnabled())
	}
	if _, err := Load(mapLookup(map[string]string{"WAZZAP_AGENT_ENABLED": "true"})); err == nil {
		t.Fatal("agent was enabled without the WhatsApp runtime")
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{name: "root data directory", env: map[string]string{"WAZZAP_DATA_DIR": filepath.VolumeName(filepath.Clean(string(filepath.Separator))) + string(filepath.Separator)}},
		{name: "invalid address", env: map[string]string{"WAZZAP_HTTP_ADDRESS": "localhost"}},
		{name: "invalid log level", env: map[string]string{"WAZZAP_LOG_LEVEL": "trace"}},
		{name: "invalid log format", env: map[string]string{"WAZZAP_LOG_FORMAT": "yaml"}},
		{name: "invalid shutdown timeout", env: map[string]string{"WAZZAP_SHUTDOWN_TIMEOUT": "0s"}},
		{name: "excessive shutdown timeout", env: map[string]string{"WAZZAP_SHUTDOWN_TIMEOUT": "6m"}},
		{name: "zero response limit", env: map[string]string{"WAZZAP_MAX_RESPONSE_BYTES": "0"}},
		{name: "excessive response limit", env: map[string]string{"WAZZAP_MAX_RESPONSE_BYTES": "16385"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Load(mapLookup(test.env))
			if err == nil {
				t.Fatal("Load succeeded, want validation error")
			}
		})
	}
}

func TestSnapshotRedactsSecret(t *testing.T) {
	secret := "secret-value-that-must-not-leak"
	cfg, err := Load(mapLookup(map[string]string{
		"WAZZAP_LLM_API_KEY":      secret,
		"WAZZAP_SHUTDOWN_TIMEOUT": "12s",
	}))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.LLMAPIKey() != secret {
		t.Fatal("secret accessor did not return configured value")
	}
	redacted := cfg.Redacted()
	if redacted["llm_api_key_configured"] != true {
		t.Fatalf("configured marker = %#v, want true", redacted["llm_api_key_configured"])
	}
	for _, value := range redacted {
		if value == secret {
			t.Fatal("redacted config exposed secret")
		}
	}
	if cfg.ShutdownTimeout() != 12*time.Second {
		t.Fatalf("shutdown timeout = %s, want 12s", cfg.ShutdownTimeout())
	}
}

func mapLookup(values map[string]string) LookupEnv {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}
