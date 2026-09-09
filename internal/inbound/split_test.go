package inbound

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

type laneFunc func(context.Context, conversation.IncomingMessage) error

func (function laneFunc) Resume(ctx context.Context, message conversation.IncomingMessage) error {
	return function(ctx, message)
}

func TestCommandLaneProgressesWhileAILaneIsBlocked(t *testing.T) {
	aiStarted := make(chan struct{})
	releaseAI := make(chan struct{})
	commandDone := make(chan struct{})
	var aiCalls atomic.Int32
	dispatcher := &SplitDispatcher{
		command: laneFunc(func(context.Context, conversation.IncomingMessage) error { close(commandDone); return nil }),
		ai: laneFunc(func(context.Context, conversation.IncomingMessage) error {
			aiCalls.Add(1)
			close(aiStarted)
			<-releaseAI
			return nil
		}),
		commandQueue: make(chan conversation.IncomingMessage, 1), aiQueue: make(chan conversation.IncomingMessage, 1),
		commandWorkers: 1, aiWorkers: 1, report: func(Lane, error) {},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = dispatcher.Run(ctx); close(done) }()
	if err := dispatcher.Resume(ctx, validSplitMessage(t, "hello")); err != nil {
		t.Fatalf("route AI message: %v", err)
	}
	select {
	case <-aiStarted:
	case <-time.After(time.Second):
		t.Fatal("AI lane did not start")
	}
	if err := dispatcher.Resume(ctx, validSplitMessage(t, "/help")); err != nil {
		t.Fatalf("route command: %v", err)
	}
	select {
	case <-commandDone:
	case <-time.After(time.Second):
		t.Fatal("command lane was blocked by AI lane")
	}
	close(releaseAI)
	cancel()
	<-done
	if aiCalls.Load() != 1 {
		t.Fatalf("AI calls = %d, want 1", aiCalls.Load())
	}
}

func validSplitMessage(t *testing.T, text string) conversation.IncomingMessage {
	t.Helper()
	messageID, _ := identity.NewMessageID()
	invocationID, _ := identity.NewInvocationID()
	causationID, _ := identity.NewCausationID()
	tenantID, _ := identity.NewTenantID()
	accountID, _ := identity.NewAccountID()
	chatID, _ := identity.NewChatID()
	participantID, _ := identity.NewParticipantID()
	lid, _ := identity.ParseLID("10000000001@lid")
	ref, _ := identity.ParseSenderRef("u_01234567")
	now := time.Now().UTC()
	return conversation.IncomingMessage{
		ID: messageID, InvocationID: invocationID, CausationID: causationID,
		TenantID: tenantID, AccountID: accountID, ChatID: chatID,
		SenderID: participantID, SenderLID: lid, SenderRef: ref,
		ChatKind: conversation.ChatDirect, Text: text, OccurredAt: now, ReceivedAt: now,
	}
}
