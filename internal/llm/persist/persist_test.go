package persist_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/llm/persist"
)

type scripted struct {
	errs  []error
	calls int
}

func (model *scripted) Generate(context.Context, agent.ModelRequest) (agent.ModelResult, error) {
	index := model.calls
	model.calls++
	if index < len(model.errs) && model.errs[index] != nil {
		return agent.ModelResult{}, model.errs[index]
	}
	return agent.ModelResult{Text: "ok"}, nil
}

// fakeClock advances only when the invoker sleeps.
type fakeClock struct {
	now    time.Time
	slept  []time.Duration
	cancel context.CancelFunc
}

func (clock *fakeClock) Now() time.Time { return clock.now }
func (clock *fakeClock) Sleep(_ context.Context, delay time.Duration) error {
	clock.slept = append(clock.slept, delay)
	clock.now = clock.now.Add(delay)
	return nil
}

func newInvoker(t *testing.T, model agent.ModelInvoker) (*persist.Invoker, *fakeClock, agent.ModelRequest, func()) {
	t.Helper()
	invoker := persist.New(model)
	clock := &fakeClock{now: time.Unix(1_000_000, 0)}
	invoker.Now, invoker.Sleep = clock.Now, clock.Sleep
	id, err := identity.NewInvocationID()
	if err != nil {
		t.Fatal(err)
	}
	return invoker, clock, agent.ModelRequest{InvocationID: id}, invoker.Persist(id)
}

func rateLimited() error { return agent.NewError(agent.ErrorRateLimited, "test", errors.New("429")) }

func TestRetriesUntilTheModelAnswers(t *testing.T) {
	model := &scripted{errs: []error{rateLimited(), agent.NewError(agent.ErrorTimeout, "test", errors.New("slow")), rateLimited()}}
	invoker, clock, request, release := newInvoker(t, model)
	defer release()
	result, err := invoker.Generate(context.Background(), request)
	if err != nil || result.Text != "ok" || model.calls != 4 {
		t.Fatalf("result=%q err=%v calls=%d", result.Text, err, model.calls)
	}
	want := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}
	for index, delay := range want {
		if clock.slept[index] != delay {
			t.Fatalf("delays = %v, want %v", clock.slept, want)
		}
	}
}

func TestGivesUpAfterTheMaximumWait(t *testing.T) {
	errs := make([]error, 1000)
	for index := range errs {
		errs[index] = rateLimited()
	}
	model := &scripted{errs: errs}
	invoker, clock, request, release := newInvoker(t, model)
	defer release()
	_, err := invoker.Generate(context.Background(), request)
	if !agent.IsCode(err, agent.ErrorRateLimited) {
		t.Fatalf("err = %v, want the last rate-limit error", err)
	}
	elapsed := clock.now.Sub(time.Unix(1_000_000, 0))
	if elapsed > persist.DefaultMaxWait || elapsed < persist.DefaultMaxWait-30*time.Second {
		t.Fatalf("waited %s, want just under %s", elapsed, persist.DefaultMaxWait)
	}
}

func TestDoesNotRetryAPermanentFailure(t *testing.T) {
	model := &scripted{errs: []error{agent.NewError(agent.ErrorPermissionDenied, "test", errors.New("no"))}}
	invoker, _, request, release := newInvoker(t, model)
	defer release()
	if _, err := invoker.Generate(context.Background(), request); !agent.IsCode(err, agent.ErrorPermissionDenied) || model.calls != 1 {
		t.Fatalf("err=%v calls=%d", err, model.calls)
	}
}

func TestOtherInvocationsAreNotRetried(t *testing.T) {
	model := &scripted{errs: []error{rateLimited()}}
	invoker, _, _, release := newInvoker(t, model)
	defer release()
	other, _ := identity.NewInvocationID()
	if _, err := invoker.Generate(context.Background(), agent.ModelRequest{InvocationID: other}); !agent.IsCode(err, agent.ErrorRateLimited) || model.calls != 1 {
		t.Fatalf("err=%v calls=%d", err, model.calls)
	}
}

func TestStopsWhenTheContextIsCancelled(t *testing.T) {
	model := &scripted{errs: []error{rateLimited(), rateLimited(), rateLimited()}}
	invoker, _, request, release := newInvoker(t, model)
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	invoker.Sleep = func(context.Context, time.Duration) error { cancel(); return ctx.Err() }
	if _, err := invoker.Generate(ctx, request); err == nil || model.calls != 1 {
		t.Fatalf("err=%v calls=%d", err, model.calls)
	}
}

func TestReleaseStopsRetrying(t *testing.T) {
	model := &scripted{errs: []error{rateLimited()}}
	invoker, _, request, release := newInvoker(t, model)
	release()
	if _, err := invoker.Generate(context.Background(), request); err == nil || model.calls != 1 {
		t.Fatalf("err=%v calls=%d", err, model.calls)
	}
}
