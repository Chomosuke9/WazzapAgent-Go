package observability

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"
)

// LogEntry is a safe, presentation-ready application log item. Sensitive and
// opaque provider fields are deliberately excluded before an entry is stored.
type LogEntry struct {
	Time    string
	Level   string
	Message string
	Details string
}

// LogBuffer keeps the newest application events for the in-app log viewer.
type LogBuffer struct {
	mu       sync.RWMutex
	capacity int
	entries  []LogEntry
}

func NewLogBuffer(capacity int) *LogBuffer {
	if capacity < 1 {
		capacity = 500
	}
	return &LogBuffer{capacity: capacity, entries: make([]LogEntry, 0, capacity)}
}

func (buffer *LogBuffer) Record(level, message, details string) {
	if buffer == nil {
		return
	}
	entry := LogEntry{
		Time:    time.Now().UTC().Format(time.RFC3339Nano),
		Level:   strings.ToUpper(strings.TrimSpace(level)),
		Message: sanitizeLogText(message),
		Details: sanitizeLogText(details),
	}
	if entry.Level == "" {
		entry.Level = "INFO"
	}
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	if len(buffer.entries) == buffer.capacity {
		copy(buffer.entries, buffer.entries[1:])
		buffer.entries[len(buffer.entries)-1] = entry
		return
	}
	buffer.entries = append(buffer.entries, entry)
}

func (buffer *LogBuffer) Entries() []LogEntry {
	if buffer == nil {
		return nil
	}
	buffer.mu.RLock()
	defer buffer.mu.RUnlock()
	return append([]LogEntry(nil), buffer.entries...)
}

func (buffer *LogBuffer) Handler() slog.Handler {
	return &logBufferHandler{buffer: buffer}
}

// MultiHandler fans slog records out to each configured handler. It lets the
// desktop keep its normal diagnostic output while also feeding the UI buffer.
type MultiHandler struct {
	handlers []slog.Handler
}

func NewMultiHandler(handlers ...slog.Handler) slog.Handler {
	filtered := make([]slog.Handler, 0, len(handlers))
	for _, handler := range handlers {
		if handler != nil {
			filtered = append(filtered, handler)
		}
	}
	return &MultiHandler{handlers: filtered}
}

func (handler *MultiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, child := range handler.handlers {
		if child.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (handler *MultiHandler) Handle(ctx context.Context, record slog.Record) error {
	var failures []error
	for _, child := range handler.handlers {
		if child.Enabled(ctx, record.Level) {
			if err := child.Handle(ctx, record); err != nil {
				failures = append(failures, err)
			}
		}
	}
	return errorsJoin(failures)
}

func (handler *MultiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	children := make([]slog.Handler, len(handler.handlers))
	for index, child := range handler.handlers {
		children[index] = child.WithAttrs(attrs)
	}
	return &MultiHandler{handlers: children}
}

func (handler *MultiHandler) WithGroup(name string) slog.Handler {
	children := make([]slog.Handler, len(handler.handlers))
	for index, child := range handler.handlers {
		children[index] = child.WithGroup(name)
	}
	return &MultiHandler{handlers: children}
}

type logBufferHandler struct {
	buffer *LogBuffer
	attrs  []slog.Attr
}

func (handler *logBufferHandler) Enabled(_ context.Context, level slog.Level) bool {
	return handler.buffer != nil && level >= slog.LevelInfo
}

func (handler *logBufferHandler) Handle(_ context.Context, record slog.Record) error {
	if handler.buffer == nil {
		return nil
	}
	attrs := append([]slog.Attr(nil), handler.attrs...)
	record.Attrs(func(attr slog.Attr) bool {
		attrs = append(attrs, attr)
		return true
	})
	details := make([]string, 0, len(attrs))
	for _, attr := range attrs {
		if !safeLogAttribute(attr.Key) {
			continue
		}
		value := attr.Value.Resolve().String()
		if value == "" {
			continue
		}
		details = append(details, attr.Key+"="+value)
	}
	handler.buffer.Record(logLevelName(record.Level), record.Message, strings.Join(details, " · "))
	return nil
}

func (handler *logBufferHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	copy := *handler
	copy.attrs = append(append([]slog.Attr(nil), handler.attrs...), attrs...)
	return &copy
}

func (handler *logBufferHandler) WithGroup(_ string) slog.Handler {
	return handler
}

func safeLogAttribute(key string) bool {
	switch strings.ToLower(key) {
	case "chat_name", "chatname", "sender", "trigger", "batch", "provider", "model", "elapsed", "response_bytes", "effects", "code", "reason", "state", "binding_state", "bindingstate", "method", "operation":
		return true
	default:
		return false
	}
}

func logLevelName(level slog.Level) string {
	switch {
	case level >= slog.LevelError:
		return "ERROR"
	case level >= slog.LevelWarn:
		return "WARN"
	case level >= slog.LevelInfo:
		return "INFO"
	default:
		return "DEBUG"
	}
}

var sensitiveLogValue = regexp.MustCompile(`(?i)(api[_ -]?key|access[_ -]?token|refresh[_ -]?token|token|password|secret|authorization)\s*[:=]\s*("[^"]*"|'[^']*'|[^\s,;]+)`)
var bearerLogValue = regexp.MustCompile(`(?i)\bbearer\s+[a-z0-9._~+/=-]+`)

func sanitizeLogText(value string) string {
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	value = sensitiveLogValue.ReplaceAllString(value, "$1=<redacted>")
	value = bearerLogValue.ReplaceAllString(value, "Bearer <redacted>")
	value = strings.TrimSpace(value)
	if runes := []rune(value); len(runes) > 600 {
		value = string(runes[:600]) + "…"
	}
	return value
}

func errorsJoin(failures []error) error {
	if len(failures) == 0 {
		return nil
	}
	return fmt.Errorf("log handlers: %v", failures)
}
