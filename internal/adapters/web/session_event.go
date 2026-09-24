package web

import (
	"fmt"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/control"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/observability"
)

// The browser status page polls the controller. Events still enter the safe log.
type SessionEventSink struct{ Logs *observability.LogBuffer }

func (sink SessionEventSink) TryPublish(event control.SessionEvent) bool {
	if sink.Logs != nil {
		details := fmt.Sprintf("state=%s · binding_state=%s", event.Status.RuntimeState, event.Status.BindingState)
		if event.Status.ErrorCode != "" {
			details += " · code=" + string(event.Status.ErrorCode)
			sink.Logs.Record("ERROR", "WhatsApp session status reported a failure", details)
		} else {
			sink.Logs.Record("INFO", "WhatsApp session status changed", details)
		}
	}
	return true
}
