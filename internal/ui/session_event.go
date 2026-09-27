package ui

import (
	"fmt"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/control"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/observability"
)

// SessionLog records WhatsApp session status changes in the UI log. The UI
// itself polls GetWhatsAppSessionStatus, so nothing is pushed to the frontend.
type SessionLog struct{ Logs *observability.LogBuffer }

// TryPublish writes a safe summary of the status change. It never records
// pairing codes or QR payloads.
func (sink SessionLog) TryPublish(event control.SessionEvent) bool {
	if sink.Logs == nil {
		return true
	}
	details := fmt.Sprintf("state=%s · binding_state=%s", event.Status.RuntimeState, event.Status.BindingState)
	if event.Status.ErrorCode != "" {
		details += " · code=" + string(event.Status.ErrorCode)
		sink.Logs.Record("ERROR", "WhatsApp session status reported a failure", details)
		return true
	}
	sink.Logs.Record("INFO", "WhatsApp session status changed", details)
	return true
}

var _ control.SessionEventSink = SessionLog{}
