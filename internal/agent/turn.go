package agent

import (
	"context"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

type TurnLease string

type TurnState uint8

const (
	TurnGenerating TurnState = iota + 1
	TurnFailedRetryable
	TurnResponsePlanned
	TurnDeliveryPending
	TurnSucceeded
	TurnFailedTerminal
	TurnUnknownOutcome
)

type DeliveryStatus uint8

const (
	DeliveryNotStarted DeliveryStatus = iota
	DeliveryPending
	DeliverySucceeded
	DeliveryFailedTerminal
	DeliveryUnknownOutcome
)

type ClaimTurnRequest struct {
	Key        Key
	Invocation Invocation
	Digest     InvocationDigest
	Now        time.Time
}

type TurnClaim struct {
	State TurnState
	Lease TurnLease
	Plan  *StoredPlan
}

type CommitPlanRequest struct {
	Key           Key
	InvocationID  identity.InvocationID
	Lease         TurnLease
	ConfigVersion ConfigVersion
	ResponseText  string
}

type FailGenerationRequest struct {
	Key          Key
	InvocationID identity.InvocationID
	Lease        TurnLease
	Code         ErrorCode
	Retryable    bool
	RetryAfter   time.Time
}

type DispatchRef struct {
	Key      Key
	ActionID identity.ActionID
}

type StoredPlan struct {
	InvocationID  identity.InvocationID
	ConfigVersion ConfigVersion
	ResponseID    identity.MessageID
	ActionID      identity.ActionID
	Text          string
	Dispatch      DispatchRef
}

type TurnRecord struct {
	Key          Key
	InvocationID identity.InvocationID
	Digest       InvocationDigest
	State        TurnState
	Plan         *StoredPlan
	Delivery     DeliveryStatus
	UpdatedAt    time.Time
}

type TurnStore interface {
	Claim(context.Context, ClaimTurnRequest) (TurnClaim, error)
	CommitPlan(context.Context, CommitPlanRequest) (StoredPlan, error)
	FailGeneration(context.Context, FailGenerationRequest) error
	Load(context.Context, Key, identity.InvocationID) (TurnRecord, error)
}

type DeliveryResult struct {
	ActionID    identity.ActionID
	Status      DeliveryStatus
	CompletedAt *time.Time
}

type ResponseDispatcher interface {
	Dispatch(context.Context, DispatchRef) (DeliveryResult, error)
}

type InvokeResult struct {
	InvocationID  identity.InvocationID
	ConfigVersion ConfigVersion
	ResponseID    identity.MessageID
	ActionID      identity.ActionID
	Text          string
	Delivery      DeliveryStatus
}

func clonePlan(plan *StoredPlan) *StoredPlan {
	if plan == nil {
		return nil
	}
	copyPlan := *plan
	return &copyPlan
}
