package observability

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	langsmith "github.com/langchain-ai/langsmith-go"
	"github.com/langchain-ai/langsmith-go/instrumentation/traceopenai"
)

const (
	langSmithProjectName = "wazzapagent"
	langSmithServiceName = "wazzapagent"
)

// LangSmith owns the optional LangSmith tracer used by the model HTTP client.
// A zero-value instance is disabled and leaves HTTP clients untouched.
type LangSmith struct {
	tracer *langsmith.OTelTracer
}

// NewLangSmith creates an optional LangSmith tracer. An empty API key disables
// tracing without creating an exporter or wrapping the model HTTP client.
func NewLangSmith(apiKey string) (*LangSmith, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return &LangSmith{}, nil
	}

	tracer, err := langsmith.NewTracer(
		langsmith.WithAPIKey(apiKey),
		langsmith.WithProjectName(langSmithProjectName),
		langsmith.WithServiceName(langSmithServiceName),
	)
	if err != nil {
		return nil, fmt.Errorf("create LangSmith tracer: %w", err)
	}
	return &LangSmith{tracer: tracer}, nil
}

// Enabled reports whether this instance has an active LangSmith tracer.
func (tracing *LangSmith) Enabled() bool {
	return tracing != nil && tracing.tracer != nil
}

// WrapHTTPClient adds automatic OpenAI-compatible request tracing when
// enabled. Disabled tracing returns the exact client passed by the caller.
func (tracing *LangSmith) WrapHTTPClient(client *http.Client) *http.Client {
	if !tracing.Enabled() {
		return client
	}
	return traceopenai.WrapClient(client, traceopenai.WithTracerProvider(tracing.tracer.TracerProvider()))
}

// Shutdown flushes pending spans. It is a no-op when tracing is disabled.
func (tracing *LangSmith) Shutdown(ctx context.Context) error {
	if !tracing.Enabled() {
		return nil
	}
	return tracing.tracer.Shutdown(ctx)
}
