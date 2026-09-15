package observability

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
)

type CompactHandler struct {
	opts  slog.HandlerOptions
	mu    *sync.Mutex
	w     io.Writer
	attrs []slog.Attr
}

func NewCompactHandler(w io.Writer, opts *slog.HandlerOptions) *CompactHandler {
	if opts == nil {
		opts = &slog.HandlerOptions{}
	}
	return &CompactHandler{
		opts: *opts,
		mu:   &sync.Mutex{},
		w:    w,
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
	buf = append(buf, t...)
	buf = append(buf, ' ')

	// Level: INF, WRN, ERR, DBG
	level := levelString(r.Level)
	buf = append(buf, level...)
	buf = append(buf, ' ')

	// Check for chat_name attribute for prefix
	var chatName string
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "chat_name" {
			chatName = a.Value.String()
			return false
		}
		return true
	})

	// Add prefix if chat_name exists
	if chatName != "" {
		prefix := truncateAndPad(chatName, 20)
		buf = append(buf, '[')
		buf = append(buf, prefix...)
		buf = append(buf, "] "...)
	}

	// Message
	buf = append(buf, r.Message...)

	// Attributes
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "chat_name" {
			return true
		}
		buf = append(buf, ' ')
		buf = append(buf, a.Key...)
		buf = append(buf, '=')
		buf = append(buf, a.Value.String()...)
		return true
	})

	// Handler-level attributes (like instance_id)
	for _, attr := range h.attrs {
		if attr.Key == "instance_id" {
			buf = append(buf, " inst="...)
			buf = append(buf, attr.Value.String()...)
		} else {
			buf = append(buf, ' ')
			buf = append(buf, attr.Key...)
			buf = append(buf, '=')
			buf = append(buf, attr.Value.String()...)
		}
	}

	buf = append(buf, '\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := h.w.Write(buf)
	return err
}

func (h *CompactHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	h2 := *h
	h2.attrs = append(h2.attrs, attrs...)
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
