// Package typesafe calls TypeSafe's System One API, which answers typed
// questions (yes/no, choice, score) about text or JSON state with
// probabilities instead of generated text.
package typesafe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// DefaultEndpoint is System One, where Jev answers the questions.
	DefaultEndpoint = "https://api.typesafe.ai/v1/systemone"
	DefaultModel    = "jev-latest"
	maxBodyBytes    = 1 << 20
)

// Question is one named judgment. Type is "noul", "choice" or "score";
// Instructions and Criteria may be text or JSON, as the API accepts.
type Question struct {
	Type         string `json:"type"`
	Instructions any    `json:"instructions,omitempty"`
	Criteria     any    `json:"criteria,omitempty"`
}

// NoulCriteria describes the yes and no outcomes of a noul question.
type NoulCriteria struct {
	True  any `json:"true,omitempty"`
	False any `json:"false,omitempty"`
}

// Answer holds whichever fields the question's type returns: Noul is the
// probability of yes; Choice and Score come with Confidence and per-label
// Probabilities.
type Answer struct {
	Type          string             `json:"type"`
	Noul          float64            `json:"noul"`
	Choice        string             `json:"choice"`
	Score         float64            `json:"score"`
	Confidence    float64            `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities"`
}

type Result struct {
	Model   string            `json:"model"`
	Answers map[string]Answer `json:"answers"`
	Usage   struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

type Client struct {
	endpoint   string
	apiKey     string
	model      string
	httpClient *http.Client
}

// New returns a client that posts to endpoint (DefaultEndpoint when empty)
// with the given API key. A nil httpClient gets a 10-second timeout, the
// official SDKs' per-attempt default.
func New(apiKey, endpoint string, httpClient *http.Client) (*Client, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("typesafe: API key is required")
	}
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	if parsed, err := url.Parse(endpoint); err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("typesafe: endpoint %q must be an absolute HTTP(S) URL", endpoint)
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{endpoint: endpoint, apiKey: strings.TrimSpace(apiKey), model: DefaultModel, httpClient: httpClient}, nil
}

// SystemOne asks every question about state in one request. The questions
// run in parallel on the server and cannot see each other's answers.
func (client *Client) SystemOne(ctx context.Context, state any, questions map[string]Question) (Result, error) {
	if len(questions) == 0 {
		return Result{}, errors.New("typesafe: at least one question is required")
	}
	body, err := json.Marshal(struct {
		Model     string              `json:"model"`
		State     any                 `json:"state"`
		Questions map[string]Question `json:"questions"`
	}{client.model, state, questions})
	if err != nil {
		return Result{}, fmt.Errorf("typesafe: encode request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.endpoint, bytes.NewReader(body))
	if err != nil {
		return Result{}, fmt.Errorf("typesafe: build request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+client.apiKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := client.httpClient.Do(request)
	if err != nil {
		return Result{}, fmt.Errorf("typesafe: request failed: %w", err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxBodyBytes))
	if err != nil {
		return Result{}, fmt.Errorf("typesafe: read response: %w", err)
	}
	if response.StatusCode/100 != 2 {
		return Result{}, fmt.Errorf("typesafe: HTTP %d (request %s): %s", response.StatusCode,
			response.Header.Get("x-typesafe-request-id"), strings.TrimSpace(string(payload)))
	}
	var result Result
	if err := json.Unmarshal(payload, &result); err != nil {
		return Result{}, fmt.Errorf("typesafe: decode response: %w", err)
	}
	for name := range questions {
		if _, ok := result.Answers[name]; !ok {
			return Result{}, fmt.Errorf("typesafe: response has no answer for %q", name)
		}
	}
	return result, nil
}
