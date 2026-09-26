//go:build gui

package wails

import (
	"github.com/Chomosuke9/WazzapAgent-Go/internal/control"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/observability"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/ui"
	"github.com/wailsapp/wails/v3/pkg/application"
)

const WhatsAppSessionEventName = ui.WhatsAppSessionEventName

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
		ui.RecordSessionEvent(sink.logs, event)
		sink.app.Event.Emit(WhatsAppSessionEventName, ui.NewWhatsAppSessionEventDTO(event))
	}
}

var _ control.SessionEventSink = (*SessionEventSink)(nil)
