package agent

import (
	"testing"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

func TestDeliveryForTurnStatePreservesTerminalReplayStatus(t *testing.T) {
	tests := map[TurnState]DeliveryStatus{
		TurnResponsePlanned: DeliveryPending,
		TurnDeliveryPending: DeliveryPending,
		TurnSucceeded:       DeliverySucceeded,
		TurnFailedTerminal:  DeliveryFailedTerminal,
		TurnUnknownOutcome:  DeliveryUnknownOutcome,
	}
	for state, want := range tests {
		if got := deliveryForTurnState(state); got != want {
			t.Errorf("deliveryForTurnState(%d) = %d, want %d", state, got, want)
		}
	}
}

func TestCommandIntentAcceptsRegisteredCommandShape(t *testing.T) {
	target, _ := identity.NewMessageID()
	for _, command := range []string{"/group close", "/help", "/future any arguments"} {
		intent := EffectIntent{Kind: EffectRunCommand, Command: command}
		if err := intent.Validate(); err != nil {
			t.Errorf("%q rejected: %v", command, err)
		}
	}
	for _, command := range []string{"group close", " /help", ""} {
		intent := EffectIntent{Kind: EffectRunCommand, Command: command}
		if err := intent.Validate(); err == nil {
			t.Errorf("%q accepted without valid form/target", command)
		}
	}
	if err := (EffectIntent{Kind: EffectRunCommand, Command: "/group delete", TargetMessageID: target}).Validate(); err != nil {
		t.Fatalf("targeted delete rejected: %v", err)
	}
}
