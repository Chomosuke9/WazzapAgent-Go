//go:build gui

package wails

import (
	"fmt"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/control"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/observability"
	"github.com/wailsapp/wails/v3/pkg/application"
)

const WhatsAppSessionEventName = "whatsapp:session"

type SessionEventSink struct {
	app    *application.App
	events chan control.SessionEvent
	logs   *observability.LogBuffer
}

func NewSessionEventSink(app *application.App, logs ...*observability.LogBuffer) *SessionEventSink {
	sink := &SessionEventSink{app: app, events: make(chan control.SessionEvent, 1)}
	if len(logs) > 0 {
		sink.logs = logs[0]
	}
	go sink.dispatch()
	return sink
}

func (sink *SessionEventSink) TryPublish(event control.SessionEvent) bool {
	if sink == nil || sink.app == nil {
		return false
	}
	select {
	case sink.events <- event:
		return true
	default:
		// The UI needs the newest snapshot for an operation. Replacing an
		// older queued update also prevents a stale QR from surfacing after cancel.
		select {
		case <-sink.events:
		default:
		}
		select {
		case sink.events <- event:
			return true
		default:
			return false
		}
	}
}

func (sink *SessionEventSink) dispatch() {
	for event := range sink.events {
		if sink.logs != nil {
			details := fmt.Sprintf("state=%s · binding_state=%s", event.Status.RuntimeState, event.Status.BindingState)
			if event.Status.ErrorCode != "" {
				details += " · code=" + string(event.Status.ErrorCode)
				sink.logs.Record("ERROR", "WhatsApp session status reported a failure", details)
			} else {
				sink.logs.Record("INFO", "WhatsApp session status changed", details)
			}
		}
		sink.app.Event.Emit(WhatsAppSessionEventName, WhatsAppSessionEventDTO{
			OperationID: event.OperationID,
			Status:      whatsappSessionStatusDTO(event.Status),
		})
	}
}

var _ control.SessionEventSink = (*SessionEventSink)(nil)
