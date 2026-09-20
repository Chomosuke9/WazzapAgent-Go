package observability

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
)

const compactContextWidth = 18

const (
	ansiReset   = "\x1b[0m"
	ansiDim     = "\x1b[90m"
	ansiCyan    = "\x1b[36m"
	ansiGreen   = "\x1b[32m"
	ansiYellow  = "\x1b[33m"
	ansiRed     = "\x1b[31m"
	ansiMagenta = "\x1b[35m"
)

type CompactHandler struct {
	opts  slog.HandlerOptions
	mu    *sync.Mutex
	w     io.Writer
	attrs []slog.Attr
	color bool
}

func NewCompactHandler(w io.Writer, opts *slog.HandlerOptions) *CompactHandler {
	return newCompactHandler(w, opts, compactColorEnabled(w))
}

func newCompactHandler(w io.Writer, opts *slog.HandlerOptions, color bool) *CompactHandler {
	if opts == nil {
		opts = &slog.HandlerOptions{}
	}
	return &CompactHandler{
		opts:  *opts,
		mu:    &sync.Mutex{},
		w:     w,
		color: color,
	}
}

func (h *CompactHandler) Enabled(_ context.Context, level slog.Level) bool {
	minLevel := slog.LevelInfo
	if h.opts.Level != nil {
		minLevel = h.opts.Level.Level()
	}
	return level >= minLevel
}

func (h *CompactHandler) Handle(_ context.Context, r slog.Record) error {
	buf := make([]byte, 0, 1024)

	// Time: HH:MM:SS
	t := r.Time.Format("15:04:05")
	buf = appendStyled(buf, h.color, ansiDim, t)
	buf = append(buf, ' ')

	// Level: INF, WRN, ERR, DBG
	level := levelString(r.Level)
	buf = appendStyled(buf, h.color, levelColor(r.Level), level)
	buf = append(buf, ' ')

	attrs := make([]slog.Attr, 0, len(h.attrs)+r.NumAttrs())
	attrs = append(attrs, h.attrs...)
	r.Attrs(func(attr slog.Attr) bool {
		attrs = append(attrs, attr)
		return true
	})

	contextLabel, contextKey := compactContext(attrs)
	hasChatName := isChatNameKey(contextKey)
	contextText := "[" + truncateAndPad(contextLabel, compactContextWidth) + "]"
	buf = appendStyled(buf, h.color, ansiMagenta, contextText)
	buf = append(buf, ' ')

	// Message
	buf = append(buf, r.Message...)

	// Attributes. The compact terminal format deliberately omits the process
	// instance ID and the attribute already used as the visual context label.
	for _, a := range attrs {
		if a.Key == "instance_id" || isChatNameKey(a.Key) || a.Key == contextKey || (hasChatName && isChatIDKey(a.Key)) {
			continue
		}
		buf = append(buf, ' ')
		buf = append(buf, a.Key...)
		buf = append(buf, '=')
		buf = append(buf, compactValue(a.Value)...)
	}

	buf = append(buf, '\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := h.w.Write(buf)
	return err
}

func (h *CompactHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	h2 := *h
	h2.attrs = append(append([]slog.Attr(nil), h.attrs...), attrs...)
	return &h2
}

func (h *CompactHandler) WithGroup(name string) slog.Handler {
	return h
}

func levelString(level slog.Level) string {
	switch {
	case level < slog.LevelInfo:
		return "DBG"
	case level < slog.LevelWarn:
		return "INF"
	case level < slog.LevelError:
		return "WRN"
	default:
		return "ERR"
	}
}

func levelColor(level slog.Level) string {
	switch {
	case level < slog.LevelInfo:
		return ansiCyan
	case level < slog.LevelWarn:
		return ansiGreen
	case level < slog.LevelError:
		return ansiYellow
	default:
		return ansiRed
	}
}

func appendStyled(buffer []byte, enabled bool, color, value string) []byte {
	if !enabled {
		return append(buffer, value...)
	}
	buffer = append(buffer, color...)
	buffer = append(buffer, value...)
	return append(buffer, ansiReset...)
}

func compactColorEnabled(output io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("FORCE_COLOR") != "" {
		return true
	}
	file, ok := output.(*os.File)
	if !ok {
		return false
	}
	// Windows terminals and hosted consoles frequently expose stdout/stderr as
	// a pipe even though they render ANSI correctly. Compact logs are intended
	// for those interactive streams, so color them by default; NO_COLOR remains
	// the explicit opt-out for redirection to plain files.
	return file.Fd() == os.Stdout.Fd() || file.Fd() == os.Stderr.Fd()
}

func compactContext(attrs []slog.Attr) (string, string) {
	for index := len(attrs) - 1; index >= 0; index-- {
		if isChatNameKey(attrs[index].Key) {
			if value := strings.TrimSpace(attrs[index].Value.Resolve().String()); value != "" {
				return value, attrs[index].Key
			}
		}
	}
	for index := len(attrs) - 1; index >= 0; index-- {
		if isChatIDKey(attrs[index].Key) {
			if value := strings.TrimSpace(attrs[index].Value.Resolve().String()); value != "" {
				return value, attrs[index].Key
			}
		}
	}
	return "system", ""
}

func isChatNameKey(key string) bool {
	return key == "chat_name" || key == "chatName"
}

func isChatIDKey(key string) bool {
	return key == "chat_id" || key == "chatId"
}

func compactValue(value slog.Value) string {
	value = value.Resolve()
	if value.Kind() == slog.KindAny {
		if err, ok := value.Any().(error); ok && err != nil {
			return fullError(err)
		}
	}
	return value.String()
}

// fullError keeps the normal unwrap chain readable and also preserves richer
// %+v details (for example a native stack trace) when an inner error provides
// them. Generic operation labels must never replace the native failure.
func fullError(err error) string {
	if err == nil {
		return "<nil>"
	}
	rendered := fmt.Sprintf("%+v", err)
	if rendered != err.Error() {
		return rendered
	}
	for cause := unwrapOne(err); cause != nil; cause = unwrapOne(cause) {
		detail := fmt.Sprintf("%+v", cause)
		if detail != cause.Error() {
			return rendered + "\ncaused by: " + detail
		}
	}
	return rendered
}

func unwrapOne(err error) error {
	type singleUnwrapper interface {
		Unwrap() error
	}
	if wrapped, ok := err.(singleUnwrapper); ok {
		return wrapped.Unwrap()
	}
	return nil
}

func truncateAndPad(s string, width int) string {
	runes := []rune(s)
	if len(runes) > width {
		if width > 3 {
			return string(runes[:width-3]) + "..."
		}
		return string(runes[:width])
	}
	return fmt.Sprintf("%-*s", width, s)
}
