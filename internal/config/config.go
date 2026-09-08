package config

import (
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	defaultDataDir         = "./data"
	defaultHTTPAddress     = "127.0.0.1:8080"
	defaultLogLevel        = "info"
	defaultLogFormat       = "json"
	defaultShutdownTimeout = 30 * time.Second
	maxShutdownTimeout     = 5 * time.Minute
)

type LookupEnv func(string) (string, bool)

type Snapshot struct {
	dataDir         string
	httpAddress     string
	logLevel        string
	logFormat       string
	shutdownTimeout time.Duration
	llmAPIKey       string
}

func Load(lookup LookupEnv) (Snapshot, error) {
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
	if !oneOf(logFormat, "json", "text") {
		return Snapshot{}, fmt.Errorf("WAZZAP_LOG_FORMAT: unsupported value %q", logFormat)
	}

	shutdownTimeout, err := parseDuration(lookup, "WAZZAP_SHUTDOWN_TIMEOUT", defaultShutdownTimeout)
	if err != nil {
		return Snapshot{}, err
	}
	if shutdownTimeout <= 0 || shutdownTimeout > maxShutdownTimeout {
		return Snapshot{}, fmt.Errorf("WAZZAP_SHUTDOWN_TIMEOUT: must be greater than zero and at most %s", maxShutdownTimeout)
	}

	llmAPIKey, _ := lookup("WAZZAP_LLM_API_KEY")

	return Snapshot{
		dataDir:         dataDir,
		httpAddress:     httpAddress,
		logLevel:        logLevel,
		logFormat:       logFormat,
		shutdownTimeout: shutdownTimeout,
		llmAPIKey:       llmAPIKey,
	}, nil
}

func (s Snapshot) DataDir() string {
	return s.dataDir
}

func (s Snapshot) HTTPAddress() string {
	return s.httpAddress
}

func (s Snapshot) LogLevel() string {
	return s.logLevel
}

func (s Snapshot) LogFormat() string {
	return s.logFormat
}

func (s Snapshot) ShutdownTimeout() time.Duration {
	return s.shutdownTimeout
}

func (s Snapshot) LLMAPIKey() string {
	return s.llmAPIKey
}

func (s Snapshot) Redacted() map[string]any {
	return map[string]any{
		"data_dir":               s.dataDir,
		"http_address":           s.httpAddress,
		"log_level":              s.logLevel,
		"log_format":             s.logFormat,
		"shutdown_timeout":       s.shutdownTimeout.String(),
		"llm_api_key_configured": s.llmAPIKey != "",
	}
}

func valueOrDefault(lookup LookupEnv, key, fallback string) string {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
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

func parseDuration(lookup LookupEnv, key string, fallback time.Duration) (time.Duration, error) {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	duration, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return duration, nil
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}
