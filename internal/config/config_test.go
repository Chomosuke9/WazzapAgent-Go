package config

import (
	"path/filepath"
	"testing"
	"time"
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
