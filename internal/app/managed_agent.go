package app

import (
	"context"
	"log/slog"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/config"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/control"
)

// ManagedAgentRuntimeFactory hands the GUI controller a headless Application.
// It does not start the diagnostics HTTP listener.
type ManagedAgentRuntimeFactory struct {
	Logger *slog.Logger
}

func (factory ManagedAgentRuntimeFactory) OpenAgentRuntime(_ context.Context, snapshot config.Snapshot) (control.ManagedAgentRuntime, error) {
	return New(snapshot, factory.Logger, Options{SystemPolicy: RenderSystemPolicy(snapshot.AssistantName())}), nil
}

// Snapshot reports WhatsApp connection state for the GUI controller.
func (application *Application) Snapshot() control.AgentRuntimeSnapshot {
	result := control.AgentRuntimeSnapshot{WhatsAppState: "stopped"}
	if accountSnapshot, ok := application.WhatsAppSnapshot(); ok {
		result.WhatsAppState = accountSnapshot.State.String()
		result.WhatsAppErrorCode = accountSnapshot.ErrorCode
	}
	return result
}
