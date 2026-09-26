package hypermeow

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/mdp/qrterminal/v3"
)

// TerminalPairingSink intentionally bypasses structured logging. It should be
// constructed only when terminal pairing output is enabled.
type TerminalPairingSink struct {
	Writer io.Writer
	mu     sync.Mutex
}

func (sink *TerminalPairingSink) ShowPairingCode(code string, validFor time.Duration) error {
	if sink == nil || sink.Writer == nil {
		return errors.New("pairing output is unavailable")
	}
	if code == "" || len(code) > 2048 || validFor <= 0 {
		return errors.New("pairing payload or lifetime is invalid")
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	checked := &errorTrackingWriter{writer: sink.Writer}
	_, _ = fmt.Fprintf(checked, "\nWhatsApp pairing QR (sensitive, valid for about %s):\n", validFor.Round(time.Second))
	qrterminal.GenerateHalfBlock(code, qrterminal.L, checked)
	_, _ = fmt.Fprintln(checked, "Scan from WhatsApp > Linked devices. Do not share this QR.")
	return checked.err
}

type errorTrackingWriter struct {
	writer io.Writer
	err    error
}

func (writer *errorTrackingWriter) Write(payload []byte) (int, error) {
	if writer.err != nil {
		return 0, writer.err
	}
	written, err := writer.writer.Write(payload)
	if err != nil {
		writer.err = err
	}
	return written, err
}
