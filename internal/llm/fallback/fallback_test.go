package fallback_test

import (
	"context"
	"testing"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/llm/fallback"
)

func TestFallbackOnlyAdvancesOnSafeModelFailures(t *testing.T) {
	first := &fakeModel{err: agent.NewError(agent.ErrorUnavailable, "first", context.DeadlineExceeded)}
	second := &fakeModel{result: agent.ModelResult{Text: "from second"}}
	invoker, err := fallback.New([]fallback.Candidate{{Name: "first", Model: first}, {Name: "second", Model: second}})
	if err != nil {
		t.Fatalf("create fallback: %v", err)
	}
	result, err := invoker.Generate(context.Background(), agent.ModelRequest{})
	if err != nil || result.Text != "from second" || first.calls != 1 || second.calls != 1 {
		t.Fatalf("fallback result/calls = %#v/%v/%d/%d", result, err, first.calls, second.calls)
	}
}

func TestFallbackDoesNotAdvanceOnValidationOrPermissionFailure(t *testing.T) {
	first := &fakeModel{err: agent.NewError(agent.ErrorPermissionDenied, "first", context.Canceled)}
	second := &fakeModel{result: agent.ModelResult{Text: "must not run"}}
	invoker, _ := fallback.New([]fallback.Candidate{{Name: "first", Model: first}, {Name: "second", Model: second}})
	_, err := invoker.Generate(context.Background(), agent.ModelRequest{})
	if !agent.IsCode(err, agent.ErrorPermissionDenied) || first.calls != 1 || second.calls != 0 {
		t.Fatalf("non-retryable fallback = %v, calls=%d/%d", err, first.calls, second.calls)
	}
}

type fakeModel struct {
	calls  int
	result agent.ModelResult
	err    error
}

func (model *fakeModel) Generate(context.Context, agent.ModelRequest) (agent.ModelResult, error) {
	model.calls++
	return model.result, model.err
}
