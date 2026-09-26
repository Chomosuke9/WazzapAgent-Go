package web

import (
	"github.com/Chomosuke9/WazzapAgent-Go/internal/control"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/observability"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/ui"
)

// The browser status page polls the controller. Events still enter the safe log.
type SessionEventSink struct{ Logs *observability.LogBuffer }

func (sink SessionEventSink) TryPublish(event control.SessionEvent) bool {
	ui.RecordSessionEvent(sink.Logs, event)
	return true
}
