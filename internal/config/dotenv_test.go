package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseDotEnvIsBoundedAndStripsBOM(t *testing.T) {
	values, err := ParseDotEnv("\ufeffA=one\nB=\"line\\nnext\"\n")
	if err != nil {
		t.Fatalf("parse dotenv: %v", err)
	}
	want := map[string]string{"A": "one", "B": "line\nnext"}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("parsed values = %#v, want %#v", values, want)
	}
	if _, err := ParseDotEnv(strings.Repeat("x", MaxDotEnvBytes+1)); err == nil {
		t.Fatal("ParseDotEnv accepted oversized content")
	}
}

func TestReadDotEnvFileDoesNotUseEmbeddedFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.env")
	values, err := ReadDotEnvFile(path, false)
	if err != nil {
		t.Fatalf("read missing optional dotenv: %v", err)
	}
	if len(values) != 0 {
		t.Fatalf("missing optional dotenv = %#v, want empty", values)
	}
	if _, err := ReadDotEnvFile(path, true); err == nil {
		t.Fatal("read required missing dotenv succeeded")
	}
	osPath := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(osPath, []byte("A=value\n"), 0o600); err != nil {
		t.Fatalf("write dotenv: %v", err)
	}
	values, err = ReadDotEnvFile(osPath, true)
	if err != nil || values["A"] != "value" {
		t.Fatalf("read dotenv = %#v, %v", values, err)
	}
}

func TestSerializeDotEnvIsCanonicalAndSafeByDefault(t *testing.T) {
	values := map[string]string{
		"WAZZAP_LLM_API_KEY": "secret",
		"WAZZAP_TENANT_ID":   "tenant",
		"Z":                  "line\nquote \" slash \\$ unicode ✓",
		"A":                  "",
	}
	got, err := SerializeDotEnv(values, DotEnvExportOptions{})
	if err != nil {
		t.Fatalf("serialize dotenv: %v", err)
	}
	want := "A=\"\"\nZ=\"line\\nquote \\\" slash " + strings.Repeat("\\", 3) + "$ unicode ✓\"\n"
	if string(got) != want {
		t.Fatalf("serialized dotenv = %q, want %q", got, want)
	}
	parsed, err := ParseDotEnv(string(got))
	if err != nil {
		t.Fatalf("parse serialized dotenv: %v", err)
	}
	if parsed["Z"] != values["Z"] || parsed["A"] != "" {
		t.Fatalf("round-trip values = %#v", parsed)
	}
	withSensitive, err := ExportDotEnv(values, DotEnvExportOptions{IncludeSecrets: true, IncludeReadOnly: true})
	if err != nil {
		t.Fatalf("serialize sensitive dotenv: %v", err)
	}
	if !strings.Contains(string(withSensitive), "WAZZAP_LLM_API_KEY=\"secret\"") || !strings.Contains(string(withSensitive), "WAZZAP_TENANT_ID=\"tenant\"") {
		t.Fatalf("opt-in export omitted sensitive values: %q", withSensitive)
	}
}
