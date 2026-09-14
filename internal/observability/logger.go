package observability

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"strings"
)

func NewLogger(output io.Writer, level, format string) (*slog.Logger, string, error) {
	if output == nil {
		return nil, "", fmt.Errorf("log output is required")
	}

	var parsedLevel slog.Level
	switch strings.ToLower(level) {
	case "debug":
		parsedLevel = slog.LevelDebug
	case "info":
		parsedLevel = slog.LevelInfo
	case "warn":
		parsedLevel = slog.LevelWarn
	case "error":
		parsedLevel = slog.LevelError
	default:
		return nil, "", fmt.Errorf("unsupported log level %q", level)
	}

	options := &slog.HandlerOptions{Level: parsedLevel}
	var handler slog.Handler
	switch strings.ToLower(format) {
	case "json":
		handler = slog.NewJSONHandler(output, options)
	case "text":
		handler = slog.NewTextHandler(output, options)
	case "compact":
		handler = NewCompactHandler(output, options)
	default:
		return nil, "", fmt.Errorf("unsupported log format %q", format)
	}

	instanceID, err := randomID()
	if err != nil {
		return nil, "", fmt.Errorf("generate instance ID: %w", err)
	}
	return slog.New(handler).With("instance_id", instanceID), instanceID, nil
}

func randomID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}
