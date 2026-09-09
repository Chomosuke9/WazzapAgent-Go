package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(mapLookup(map[string]string{"WAZZAP_WHATSAPP_ENABLED": "false"}))
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
	if cfg.WhatsAppEnabled() || cfg.AgentEnabled() {
		t.Fatalf("explicitly disabled runtime = %v/%v, want false/false", cfg.WhatsAppEnabled(), cfg.AgentEnabled())
	}
	if cfg.PairingOutput() != defaultPairingOutput {
		t.Fatalf("pairing output = %q, want %q", cfg.PairingOutput(), defaultPairingOutput)
	}
}

func TestLoadRuntimeReadsDefaultDotEnv(t *testing.T) {
	directory := t.TempDir()
	t.Chdir(directory)
	contents := strings.Join([]string{
		"# runtime configuration",
		"WAZZAP_WHATSAPP_ENABLED=false",
		"export WAZZAP_HTTP_ADDRESS = 127.0.0.1:9090",
		"WAZZAP_LOG_LEVEL=debug",
		"WAZZAP_BASE_PROMPT=Jawab pesan = dengan ringkas.",
		`WAZZAP_LLM_API_KEY="secret=with=equals"`,
		"",
	}, "\r\n")
	if err := os.WriteFile(filepath.Join(directory, defaultDotEnvPath), []byte(contents), 0o600); err != nil {
		t.Fatalf("write dotenv: %v", err)
	}

	cfg, err := LoadRuntime(mapLookup(nil))
	if err != nil {
		t.Fatalf("load runtime config: %v", err)
	}
	if cfg.HTTPAddress() != "127.0.0.1:9090" || cfg.LogLevel() != "debug" {
		t.Fatalf("runtime config = %q/%q", cfg.HTTPAddress(), cfg.LogLevel())
	}
	if cfg.BasePrompt() != "Jawab pesan = dengan ringkas." {
		t.Fatalf("base prompt = %q", cfg.BasePrompt())
	}
	if cfg.LLMAPIKey() != "secret=with=equals" {
		t.Fatal("quoted value containing equals was not loaded")
	}
}

func TestLoadRuntimeProcessEnvironmentOverridesDotEnv(t *testing.T) {
	directory := t.TempDir()
	t.Chdir(directory)
	contents := "WAZZAP_HTTP_ADDRESS=127.0.0.1:9090\nWAZZAP_LOG_LEVEL=debug\n"
	if err := os.WriteFile(filepath.Join(directory, defaultDotEnvPath), []byte(contents), 0o600); err != nil {
		t.Fatalf("write dotenv: %v", err)
	}

	cfg, err := LoadRuntime(mapLookup(map[string]string{
		"WAZZAP_WHATSAPP_ENABLED": "false",
		"WAZZAP_HTTP_ADDRESS":     "127.0.0.1:7070",
		"WAZZAP_LOG_LEVEL":        "",
	}))
	if err != nil {
		t.Fatalf("load runtime config: %v", err)
	}
	if cfg.HTTPAddress() != "127.0.0.1:7070" {
		t.Fatalf("HTTP address = %q, want process value", cfg.HTTPAddress())
	}
	if cfg.LogLevel() != defaultLogLevel {
		t.Fatalf("log level = %q, want default after explicit empty process value", cfg.LogLevel())
	}
}

func TestLoadRuntimeAllowsMissingDefaultDotEnv(t *testing.T) {
	t.Chdir(t.TempDir())
	cfg, err := LoadRuntime(mapLookup(map[string]string{"WAZZAP_WHATSAPP_ENABLED": "false"}))
	if err != nil {
		t.Fatalf("load runtime defaults: %v", err)
	}
	if cfg.HTTPAddress() != defaultHTTPAddress {
		t.Fatalf("HTTP address = %q, want %q", cfg.HTTPAddress(), defaultHTTPAddress)
	}
}

func TestLoadDataDirRuntimeIgnoresInvalidLiveRuntimeSettings(t *testing.T) {
	directory := t.TempDir()
	t.Chdir(directory)
	dataDir := filepath.Join(directory, "offline-data")
	if err := os.WriteFile(filepath.Join(directory, defaultDotEnvPath), []byte(strings.Join([]string{
		"WAZZAP_DATA_DIR=" + dataDir,
		"WAZZAP_LLM_ENDPOINT=not-a-url",
		"WAZZAP_LLM_API_KEY=",
	}, "\n")), 0o600); err != nil {
		t.Fatalf("write dotenv: %v", err)
	}
	got, err := LoadDataDirRuntime(mapLookup(nil))
	if err != nil {
		t.Fatalf("load offline data directory: %v", err)
	}
	if got != filepath.Clean(dataDir) {
		t.Fatalf("offline data directory = %q, want %q", got, filepath.Clean(dataDir))
	}
}

func TestLoadRuntimeGeneratesAndReusesStableIdentity(t *testing.T) {
	t.Chdir(t.TempDir())
	dataDir := filepath.Join(t.TempDir(), "runtime-data")
	values := enabledRuntimeValues(dataDir)

	first, err := LoadRuntime(mapLookup(values))
	if err != nil {
		t.Fatalf("first runtime load: %v", err)
	}
	if !first.WhatsAppEnabled() || !first.AgentEnabled() {
		t.Fatalf("default runtime state = %v/%v, want true/true", first.WhatsAppEnabled(), first.AgentEnabled())
	}
	if first.PairingOutput() != "terminal" {
		t.Fatalf("default pairing output = %q, want terminal", first.PairingOutput())
	}
	if first.TenantID().IsZero() || first.AccountID().IsZero() {
		t.Fatal("runtime identity was not generated")
	}

	identityPath := filepath.Join(dataDir, runtimeIdentityFilename)
	contents, err := os.ReadFile(identityPath)
	if err != nil {
		t.Fatalf("read runtime identity: %v", err)
	}
	if strings.Contains(string(contents), values["WAZZAP_LLM_API_KEY"]) {
		t.Fatal("runtime identity persisted an API key")
	}

	second, err := LoadRuntime(mapLookup(values))
	if err != nil {
		t.Fatalf("second runtime load: %v", err)
	}
	if second.TenantID() != first.TenantID() || second.AccountID() != first.AccountID() {
		t.Fatal("runtime identity changed across restarts")
	}
}

func TestLoadRuntimeAdoptsConfiguredIdentityAndRejectsConflict(t *testing.T) {
	t.Chdir(t.TempDir())
	dataDir := filepath.Join(t.TempDir(), "runtime-data")
	tenantID, _ := identity.NewTenantID()
	accountID, _ := identity.NewAccountID()
	values := enabledRuntimeValues(dataDir)
	values["WAZZAP_TENANT_ID"] = tenantID.String()
	values["WAZZAP_ACCOUNT_ID"] = accountID.String()

	configured, err := LoadRuntime(mapLookup(values))
	if err != nil {
		t.Fatalf("adopt configured identity: %v", err)
	}
	if configured.TenantID() != tenantID || configured.AccountID() != accountID {
		t.Fatal("configured identity was not adopted")
	}

	delete(values, "WAZZAP_TENANT_ID")
	delete(values, "WAZZAP_ACCOUNT_ID")
	persisted, err := LoadRuntime(mapLookup(values))
	if err != nil {
		t.Fatalf("load persisted identity: %v", err)
	}
	if persisted.TenantID() != tenantID || persisted.AccountID() != accountID {
		t.Fatal("persisted identity did not replace removed legacy configuration")
	}

	differentTenantID, _ := identity.NewTenantID()
	values["WAZZAP_TENANT_ID"] = differentTenantID.String()
	if _, err := LoadRuntime(mapLookup(values)); err == nil || !strings.Contains(err.Error(), "conflicts with the durable runtime identity") {
		t.Fatalf("identity conflict error = %v", err)
	}
}

func TestLoadRuntimeRejectsCorruptIdentityWithoutRegenerating(t *testing.T) {
	t.Chdir(t.TempDir())
	dataDir := filepath.Join(t.TempDir(), "runtime-data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatalf("create data directory: %v", err)
	}
	identityPath := filepath.Join(dataDir, runtimeIdentityFilename)
	corrupt := []byte("{not-json")
	if err := os.WriteFile(identityPath, corrupt, 0o600); err != nil {
		t.Fatalf("write corrupt identity: %v", err)
	}

	if _, err := LoadRuntime(mapLookup(enabledRuntimeValues(dataDir))); err == nil || !strings.Contains(err.Error(), "invalid JSON") {
		t.Fatalf("corrupt identity error = %v", err)
	}
	contents, err := os.ReadFile(identityPath)
	if err != nil {
		t.Fatalf("read corrupt identity: %v", err)
	}
	if string(contents) != string(corrupt) {
		t.Fatal("corrupt identity was silently replaced")
	}
}

func TestLoadRuntimeDisabledModeDoesNotCreateIdentity(t *testing.T) {
	t.Chdir(t.TempDir())
	dataDir := filepath.Join(t.TempDir(), "runtime-data")
	cfg, err := LoadRuntime(mapLookup(map[string]string{
		"WAZZAP_DATA_DIR":         dataDir,
		"WAZZAP_WHATSAPP_ENABLED": "false",
	}))
	if err != nil {
		t.Fatalf("load disabled runtime: %v", err)
	}
	if !cfg.TenantID().IsZero() || !cfg.AccountID().IsZero() {
		t.Fatal("disabled runtime unexpectedly acquired an identity")
	}
	if _, err := os.Stat(filepath.Join(dataDir, runtimeIdentityFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("disabled runtime identity stat error = %v, want not-exist", err)
	}
}

func TestLoadRuntimeRequiresExplicitDotEnv(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.env")
	_, err := LoadRuntime(mapLookup(map[string]string{dotEnvPathKey: missing}))
	if err == nil {
		t.Fatal("LoadRuntime accepted a missing explicit dotenv file")
	}
}

func TestLoadRuntimeRejectsMalformedDotEnvWithoutLeakingValue(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "broken.env")
	secret := "secret-that-must-not-leak"
	if err := os.WriteFile(path, []byte("WAZZAP_LLM_API_KEY=\""+secret+"\n"), 0o600); err != nil {
		t.Fatalf("write dotenv: %v", err)
	}

	_, err := LoadRuntime(mapLookup(map[string]string{dotEnvPathKey: path}))
	if err == nil {
		t.Fatal("LoadRuntime accepted an unterminated quoted value")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("dotenv error leaked a configured value: %v", err)
	}
}

func TestLoadRuntimeRejectsDuplicateDotEnvVariables(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "duplicate.env")
	if err := os.WriteFile(path, []byte("WAZZAP_LOG_LEVEL=info\nWAZZAP_LOG_LEVEL=debug\n"), 0o600); err != nil {
		t.Fatalf("write dotenv: %v", err)
	}

	_, err := LoadRuntime(mapLookup(map[string]string{dotEnvPathKey: path}))
	if err == nil || !strings.Contains(err.Error(), "duplicate variable WAZZAP_LOG_LEVEL") {
		t.Fatalf("duplicate dotenv error = %v", err)
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
		"WAZZAP_WHATSAPP_ENABLED": "false",
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

func enabledRuntimeValues(dataDir string) map[string]string {
	return map[string]string{
		"WAZZAP_DATA_DIR":       dataDir,
		"WAZZAP_OWNER_JID":      "15550000001@s.whatsapp.net",
		"WAZZAP_CHAT_ALLOWLIST": "15550000002@s.whatsapp.net",
		"WAZZAP_LLM_ENDPOINT":   "https://llm.example.invalid/v1/chat/completions",
		"WAZZAP_LLM_API_KEY":    "very-secret-key",
		"WAZZAP_LLM_MODEL":      "test-model",
	}
}

func mapLookup(values map[string]string) LookupEnv {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}
