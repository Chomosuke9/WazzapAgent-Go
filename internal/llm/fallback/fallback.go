// Package fallback provides a bounded, explicit LLM provider chain. It is
// deliberately a model-only concern: it is used before a turn has any native
// side effect, never as a retry mechanism for an effect dispatcher.
package fallback

import (
	"context"
	"fmt"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
)

const MaxCandidates = 4

type Candidate struct {
	Name  string
	Model agent.ModelInvoker
}

type Invoker struct{ candidates []Candidate }

func New(candidates []Candidate) (*Invoker, error) {
	if len(candidates) == 0 || len(candidates) > MaxCandidates {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "create LLM fallback", fmt.Errorf("between 1 and %d candidates are required", MaxCandidates))
	}
	copyCandidates := append([]Candidate(nil), candidates...)
	seen := make(map[string]struct{}, len(copyCandidates))
	for _, candidate := range copyCandidates {
		if candidate.Name == "" || candidate.Model == nil {
			return nil, agent.NewError(agent.ErrorInvalidArgument, "create LLM fallback", fmt.Errorf("candidate name and model are required"))
		}
		if _, exists := seen[candidate.Name]; exists {
			return nil, agent.NewError(agent.ErrorInvalidArgument, "create LLM fallback", fmt.Errorf("candidate names must be unique"))
		}
		seen[candidate.Name] = struct{}{}
	}
	return &Invoker{candidates: copyCandidates}, nil
}

func (invoker *Invoker) Generate(ctx context.Context, request agent.ModelRequest) (agent.ModelResult, error) {
	if invoker == nil || len(invoker.candidates) == 0 {
		return agent.ModelResult{}, agent.NewError(agent.ErrorUnavailable, "generate fallback model response", fmt.Errorf("no model candidate is configured"))
	}
	var lastErr error
	for _, candidate := range invoker.candidates {
		if err := ctx.Err(); err != nil {
			return agent.ModelResult{}, contextError(err)
		}
		result, err := candidate.Model.Generate(ctx, request)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if !retryableModelError(agent.CodeOf(err)) {
			return agent.ModelResult{}, err
		}
	}
	return agent.ModelResult{}, lastErr
}

func retryableModelError(code agent.ErrorCode) bool {
	switch code {
	case agent.ErrorRateLimited, agent.ErrorTimeout, agent.ErrorUnavailable, agent.ErrorProviderFailure, agent.ErrorInternal:
		return true
	default:
		return false
	}
}

func contextError(err error) error {
	if err == context.DeadlineExceeded {
		return agent.NewError(agent.ErrorTimeout, "generate fallback model response", err)
	}
	return agent.NewError(agent.ErrorCancelled, "generate fallback model response", err)
}
