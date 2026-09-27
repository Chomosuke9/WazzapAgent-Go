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

// LogEntry is a presentation-ready application log item. Details is the short
// one-line summary built from a few safe fields. Full is set on warnings and
// errors only: every attribute plus the whole error chain, so the real cause
// is never lost behind the app's own summary. Secrets are redacted in both.
type LogEntry struct {
	ID      uint64 `json:"-"`
	Time    string `json:"time"`
	Level   string `json:"level"`
	Message string `json:"message"`
	Details string `json:"details"`
	Full    string `json:"full,omitempty"`
}

// LogBuffer keeps the newest application events for the in-app log viewer.
// After Persist, warnings and errors are also kept in a file so they survive
// a restart.
type LogBuffer struct {
	mu       sync.RWMutex
	capacity int
	entries  []LogEntry
	nextID   uint64
	journal  *problemJournal
}

func NewLogBuffer(capacity int) *LogBuffer {
	if capacity < 1 {
		capacity = 500
	}
	return &LogBuffer{capacity: capacity, entries: make([]LogEntry, 0, capacity)}
}

func (buffer *LogBuffer) Record(level, message, details string) {
	buffer.record(level, message, details, "")
}

// RecordError records an entry whose full details are err's whole chain.
func (buffer *LogBuffer) RecordError(level, message, details string, err error) {
	full := ""
	if err != nil {
		full = ErrorReport(err)
	}
	buffer.record(level, message, details, full)
}

func (buffer *LogBuffer) record(level, message, details, full string) {
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
	problem := entry.Level == "WARN" || entry.Level == "ERROR"
	if problem {
		if strings.TrimSpace(full) == "" {
			// Every warning and error gets a details view, even without fields.
			full = strings.TrimSpace(message + "\n" + details)
		}
		entry.Full = sanitizeFullText(full)
	}
	buffer.mu.Lock()
	entry = buffer.push(entry)
	journal := buffer.journal
	buffer.mu.Unlock()
	if journal != nil && problem {
		journal.append(entry)
	}
}

// push stores entry under the next ID. The caller holds buffer.mu.
func (buffer *LogBuffer) push(entry LogEntry) LogEntry {
	buffer.nextID++
	entry.ID = buffer.nextID
	if len(buffer.entries) == buffer.capacity {
		copy(buffer.entries, buffer.entries[1:])
		buffer.entries[len(buffer.entries)-1] = entry
		return entry
	}
	buffer.entries = append(buffer.entries, entry)
	return entry
}

func (buffer *LogBuffer) Entries() []LogEntry {
	if buffer == nil {
		return nil
	}
	buffer.mu.RLock()
	defer buffer.mu.RUnlock()
	return append([]LogEntry(nil), buffer.entries...)
}

// Full returns the full details of the entry with id, if it is still kept.
func (buffer *LogBuffer) Full(id uint64) (string, bool) {
	if buffer == nil {
		return "", false
	}
	buffer.mu.RLock()
	defer buffer.mu.RUnlock()
	for index := len(buffer.entries) - 1; index >= 0; index-- {
		if buffer.entries[index].ID == id {
			return buffer.entries[index].Full, true
		}
	}
	return "", false
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
	full := ""
	if record.Level >= slog.LevelWarn {
		full = fullAttributes(attrs)
	}
	handler.buffer.record(logLevelName(record.Level), record.Message, strings.Join(details, " · "), full)
	return nil
}

// fullAttributes renders every attribute on its own line, with each error
// value expanded into its whole chain.
func fullAttributes(attrs []slog.Attr) string {
	var lines, errorsText []string
	for _, attr := range attrs {
		if hiddenFullAttribute(attr.Key) {
			continue
		}
		value := attr.Value.Resolve()
		if value.Kind() == slog.KindAny {
			if err, ok := value.Any().(error); ok && err != nil {
				errorsText = append(errorsText, attr.Key+":\n"+ErrorReport(err))
				continue
			}
		}
		if text := value.String(); text != "" {
			lines = append(lines, attr.Key+"="+text)
		}
	}
	return strings.Join(append(lines, errorsText...), "\n")
}

func (handler *logBufferHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	copy := *handler
	copy.attrs = append(append([]slog.Attr(nil), handler.attrs...), attrs...)
	return &copy
}

func (handler *logBufferHandler) WithGroup(_ string) slog.Handler {
	return handler
}

// hiddenFullAttribute names the fields kept out of the full details because
// they are opaque provider payloads or pairing state, not diagnostics.
func hiddenFullAttribute(key string) bool {
	switch strings.ToLower(key) {
	case "instance_id", "raw", "continuation", "qr", "qr_code", "pairing_code", "code_payload", "payload":
		return true
	default:
		return false
	}
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

// whatsAppAddress matches phone, LID and group addresses; the server part is
// kept so the kind of chat stays readable.
var whatsAppAddress = regexp.MustCompile(`\b[0-9]+(?:[-.:][0-9]+)*@(s\.whatsapp\.net|c\.us|lid|g\.us)\b`)

// whatsAppMessageID matches WhatsApp message IDs (long upper-case hex).
var whatsAppMessageID = regexp.MustCompile(`\b[0-9A-F]{16,}\b`)

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

const maxFullLogText = 16 << 10

// sanitizeFullText redacts secrets like sanitizeLogText but keeps line breaks
// and allows a much longer text, since it is only shown on request.
func sanitizeFullText(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = sensitiveLogValue.ReplaceAllString(value, "$1=<redacted>")
	value = bearerLogValue.ReplaceAllString(value, "Bearer <redacted>")
	value = RedactWhatsAppIdentifiers(value)
	value = strings.TrimSpace(value)
	if len(value) > maxFullLogText {
		value = strings.ToValidUTF8(value[:maxFullLogText], "") + "\n…(truncated)"
	}
	return value
}

// RedactWhatsAppIdentifiers hides WhatsApp addresses and message IDs in free
// text, such as a library error message.
func RedactWhatsAppIdentifiers(value string) string {
	value = whatsAppAddress.ReplaceAllString(value, "<redacted>@$1")
	return whatsAppMessageID.ReplaceAllString(value, "<message-id>")
}

func errorsJoin(failures []error) error {
	if len(failures) == 0 {
		return nil
	}
	return fmt.Errorf("log handlers: %v", failures)
}
