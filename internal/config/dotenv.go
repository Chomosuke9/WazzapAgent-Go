package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

const (
	defaultDotEnvPath = ".env"
	dotEnvPathKey     = "WAZZAP_ENV_FILE"
	MaxDotEnvBytes    = 1 << 20
	maxDotEnvBytes    = MaxDotEnvBytes
)

func lookupWithDotEnv(processLookup LookupEnv) (LookupEnv, error) {
	if processLookup == nil {
		return nil, errors.New("environment lookup is required")
	}

	pathValue, pathConfigured := processLookup(dotEnvPathKey)
	path := strings.TrimSpace(pathValue)
	explicitPath := pathConfigured && path != ""
	if !explicitPath {
		path = defaultDotEnvPath
	}
	if strings.ContainsRune(path, '\x00') {
		return nil, fmt.Errorf("%s: path must not contain a null byte", dotEnvPathKey)
	}

	fileValues, err := ReadDotEnvFile(path, explicitPath)
	if err != nil {
		return nil, fmt.Errorf("load environment file %q: %w", path, err)
	}

	return func(key string) (string, bool) {
		if value, ok := processLookup(key); ok {
			return value, true
		}
		value, ok := fileValues[key]
		return value, ok
	}, nil
}

// ReadDotEnvFile reads and parses a dotenv file with MaxDotEnvBytes and UTF-8
// bounds. A missing file is allowed only when required is false.
func ReadDotEnvFile(path string, required bool) (map[string]string, error) {
	if strings.ContainsRune(path, '\x00') {
		return nil, errors.New("path must not contain a null byte")
	}
	file, err := os.Open(path)
	if err != nil {
		if !required && errors.Is(err, os.ErrNotExist) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxDotEnvBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxDotEnvBytes {
		return nil, fmt.Errorf("file exceeds %d bytes", MaxDotEnvBytes)
	}
	if !utf8.Valid(data) {
		return nil, errors.New("file must contain valid UTF-8")
	}

	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	return ParseDotEnv(string(data))
}

// ParseDotEnv parses dotenv content without executing shell syntax. Content is
// bounded by MaxDotEnvBytes and must be valid UTF-8.
func ParseDotEnv(contents string) (map[string]string, error) {
	if len(contents) > MaxDotEnvBytes {
		return nil, fmt.Errorf("content exceeds %d bytes", MaxDotEnvBytes)
	}
	if !utf8.ValidString(contents) {
		return nil, errors.New("content must contain valid UTF-8")
	}
	contents = strings.TrimPrefix(contents, "\ufeff")
	return parseDotEnv(contents)
}

func parseDotEnv(contents string) (map[string]string, error) {
	values := make(map[string]string)
	for index, rawLine := range strings.Split(contents, "\n") {
		lineNumber := index + 1
		line := strings.TrimSpace(strings.TrimSuffix(rawLine, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.ContainsRune(line, '\x00') {
			return nil, fmt.Errorf("dotenv line %d: null bytes are not allowed", lineNumber)
		}

		line = trimExportPrefix(line)
		separator := strings.IndexByte(line, '=')
		if separator < 0 {
			return nil, fmt.Errorf("dotenv line %d: expected KEY=VALUE", lineNumber)
		}

		key := strings.TrimSpace(line[:separator])
		if !validDotEnvKey(key) {
			return nil, fmt.Errorf("dotenv line %d: invalid variable name", lineNumber)
		}
		if _, duplicate := values[key]; duplicate {
			return nil, fmt.Errorf("dotenv line %d: duplicate variable %s", lineNumber, key)
		}

		value, err := parseDotEnvValue(strings.TrimSpace(line[separator+1:]))
		if err != nil {
			return nil, fmt.Errorf("dotenv line %d: invalid value", lineNumber)
		}
		values[key] = value
	}
	return values, nil
}

func trimExportPrefix(line string) string {
	const prefix = "export"
	if len(line) > len(prefix) && strings.HasPrefix(line, prefix) {
		next := line[len(prefix)]
		if next == ' ' || next == '\t' {
			return strings.TrimSpace(line[len(prefix):])
		}
	}
	return line
}

func validDotEnvKey(key string) bool {
	if key == "" || !asciiLetterOrUnderscore(key[0]) {
		return false
	}
	for index := 1; index < len(key); index++ {
		character := key[index]
		if !asciiLetterOrUnderscore(character) && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func asciiLetterOrUnderscore(character byte) bool {
	return character == '_' || character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z'
}

func parseDotEnvValue(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	switch value[0] {
	case '\'':
		if len(value) < 2 || value[len(value)-1] != '\'' {
			return "", errors.New("unterminated single-quoted value")
		}
		return value[1 : len(value)-1], nil
	case '"':
		if len(value) < 2 || value[len(value)-1] != '"' {
			return "", errors.New("unterminated double-quoted value")
		}
		return unescapeDoubleQuoted(value[1 : len(value)-1])
	default:
		return value, nil
	}
}

func unescapeDoubleQuoted(value string) (string, error) {
	var result strings.Builder
	result.Grow(len(value))
	for index := 0; index < len(value); index++ {
		if value[index] != '\\' {
			result.WriteByte(value[index])
			continue
		}
		if index+1 >= len(value) {
			return "", errors.New("unfinished escape sequence")
		}
		index++
		switch value[index] {
		case '\\', '"', '$':
			result.WriteByte(value[index])
		case 'n':
			result.WriteByte('\n')
		case 'r':
			result.WriteByte('\r')
		case 't':
			result.WriteByte('\t')
		default:
			// Keep unknown escapes intact so quoted Windows paths such as
			// C:\\Users\\name do not lose their directory separators.
			result.WriteByte('\\')
			result.WriteByte(value[index])
		}
	}
	return result.String(), nil
}
