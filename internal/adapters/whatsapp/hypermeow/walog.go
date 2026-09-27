package hypermeow

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/observability"
	waLog "github.com/polymorfa/hypermeow/util/log"
)

// libraryLogger forwards the WhatsApp library's own warnings and errors to
// slog, so its native failure messages reach the app log instead of being
// dropped. Info and debug output is too chatty for the app log and is skipped.
// Addresses and message IDs in the library's text are redacted first.
type libraryLogger struct {
	logger *slog.Logger
	module string
}

func newLibraryLogger(logger *slog.Logger) waLog.Logger {
	if logger == nil {
		logger = slog.Default()
	}
	return libraryLogger{logger: logger, module: "whatsmeow"}
}

func (library libraryLogger) Warnf(message string, args ...any) {
	library.log(slog.LevelWarn, message, args)
}

func (library libraryLogger) Errorf(message string, args ...any) {
	library.log(slog.LevelError, message, args)
}

func (libraryLogger) Infof(string, ...any)  {}
func (libraryLogger) Debugf(string, ...any) {}

func (library libraryLogger) Sub(module string) waLog.Logger {
	return libraryLogger{logger: library.logger, module: library.module + "/" + module}
}

func (library libraryLogger) log(level slog.Level, message string, args []any) {
	ctx := context.Background()
	if !library.logger.Enabled(ctx, level) {
		return
	}
	library.logger.Log(ctx, level, "WhatsApp library: "+observability.RedactWhatsAppIdentifiers(fmt.Sprintf(message, args...)), "module", library.module)
}
