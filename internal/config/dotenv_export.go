package config

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// DotEnvExportOptions controls the two classes of values that are unsafe to
// include in a normal settings export. Both are excluded by default.
type DotEnvExportOptions struct {
	IncludeSecrets  bool
	IncludeReadOnly bool
}

// SerializeDotEnv returns a deterministic dotenv document. Keys are sorted
// lexicographically and every value is double-quoted and escaped so that the
// result can be read back by ParseDotEnv without shell evaluation.
func SerializeDotEnv(values map[string]string, options DotEnvExportOptions) ([]byte, error) {
	keys := make([]string, 0, len(values))
	for key := range values {
		if IsDotEnvSecretKey(key) && !options.IncludeSecrets {
			continue
		}
		if IsDotEnvReadOnlyKey(key) && !options.IncludeReadOnly {
			continue
		}
		if !validDotEnvKey(key) {
			return nil, fmt.Errorf("invalid variable name %q", key)
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var output bytes.Buffer
	for _, key := range keys {
		value, err := quoteDotEnvValue(values[key])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		output.WriteString(key)
		output.WriteByte('=')
		output.WriteString(value)
		output.WriteByte('\n')
		if output.Len() > MaxDotEnvBytes {
			return nil, fmt.Errorf("export exceeds %d bytes", MaxDotEnvBytes)
		}
	}
	return output.Bytes(), nil
}

// ExportDotEnv is the named export entry point used by settings adapters.
func ExportDotEnv(values map[string]string, options DotEnvExportOptions) ([]byte, error) {
	return SerializeDotEnv(values, options)
}

// IsDotEnvSecretKey reports whether a key contains a credential that is
// omitted from exports unless explicitly requested.
func IsDotEnvSecretKey(key string) bool {
	switch key {
	case "LANGSMITH_API_KEY", "WAZZAP_LLM_API_KEY", "WAZZAP_LLM_FALLBACK_API_KEY":
		return true
	default:
		return false
	}
}

// IsDotEnvReadOnlyKey reports whether a key is generated identity state rather
// than an editable runtime setting.
func IsDotEnvReadOnlyKey(key string) bool {
	switch key {
	case "WAZZAP_TENANT_ID", "WAZZAP_ACCOUNT_ID":
		return true
	default:
		return false
	}
}

func quoteDotEnvValue(value string) (string, error) {
	if !utf8.ValidString(value) {
		return "", errors.New("value must contain valid UTF-8")
	}
	var quoted strings.Builder
	quoted.Grow(len(value) + 2)
	quoted.WriteByte('"')
	for index := 0; index < len(value); index++ {
		character := value[index]
		switch character {
		case '\\', '"', '$':
			quoted.WriteByte('\\')
			quoted.WriteByte(character)
		case '\n':
			quoted.WriteString(`\n`)
		case '\r':
			quoted.WriteString(`\r`)
		case '\t':
			quoted.WriteString(`\t`)
		case 0:
			return "", errors.New("value must not contain null bytes")
		default:
			if character < 0x20 {
				return "", errors.New("value contains unsupported control character")
			}
			quoted.WriteByte(character)
		}
	}
	quoted.WriteByte('"')
	return quoted.String(), nil
}
