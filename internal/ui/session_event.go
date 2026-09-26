package ui

import (
	"fmt"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/control"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/observability"
)

// WhatsAppSessionEventName is the event name used to push session status to the UI.
const WhatsAppSessionEventName = "whatsapp:session"

// NewWhatsAppSessionEventDTO converts a controller session event into its UI payload.
func NewWhatsAppSessionEventDTO(event control.SessionEvent) WhatsAppSessionEventDTO {
	return WhatsAppSessionEventDTO{
		OperationID: event.OperationID,
		Status:      whatsappSessionStatusDTO(event.Status),
	}
}

// RecordSessionEvent writes a safe summary of a session status change to logs.
// It never records pairing codes or QR payloads.
func RecordSessionEvent(logs *observability.LogBuffer, event control.SessionEvent) {
	if logs == nil {
		return
	}
	details := fmt.Sprintf("state=%s · binding_state=%s", event.Status.RuntimeState, event.Status.BindingState)
	if event.Status.ErrorCode != "" {
		details += " · code=" + string(event.Status.ErrorCode)
		logs.Record("ERROR", "WhatsApp session status reported a failure", details)
		return
	}
	logs.Record("INFO", "WhatsApp session status changed", details)
}
