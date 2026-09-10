package observability

import (
	"context"
	"net/http"
	"testing"
)

func TestLangSmithWithoutAPIKeyLeavesHTTPClientUntouched(t *testing.T) {
	tracing, err := NewLangSmith(" ")
	if err != nil {
		t.Fatalf("create disabled tracer: %v", err)
	}
	base := &http.Client{}
	transport := base.Transport
	if got := tracing.WrapHTTPClient(base); got != base {
		t.Fatal("disabled tracing replaced the HTTP client")
	}
	if base.Transport != transport {
		t.Fatal("disabled tracing changed the HTTP transport")
	}
	if tracing.Enabled() {
		t.Fatal("disabled tracing reports enabled")
	}
	if err := tracing.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown disabled tracer: %v", err)
	}
}

func TestLangSmithWithAPIKeyWrapsHTTPClient(t *testing.T) {
	tracing, err := NewLangSmith("test-langsmith-key")
	if err != nil {
		t.Fatalf("create enabled tracer: %v", err)
	}
	defer func() {
		if err := tracing.Shutdown(context.Background()); err != nil {
			t.Fatalf("shutdown enabled tracer: %v", err)
		}
	}()

	base := &http.Client{}
	transport := base.Transport
	if got := tracing.WrapHTTPClient(base); got != base {
		t.Fatal("enabled tracing replaced the HTTP client")
	}
	if base.Transport == transport {
		t.Fatal("enabled tracing did not wrap the HTTP transport")
	}
	if !tracing.Enabled() {
		t.Fatal("enabled tracing reports disabled")
	}
}
