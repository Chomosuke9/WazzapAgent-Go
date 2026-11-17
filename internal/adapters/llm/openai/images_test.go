package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/inbound"
)

// imageServer answers every request with a reply, or with rejectStatus while
// the request carries an image. It returns the request bodies it received.
func imageServer(t *testing.T, rejectStatus int) (*Client, agent.ModelRequest, *[]string) {
	t.Helper()
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		bodies = append(bodies, string(body))
		if rejectStatus != 0 && strings.Contains(string(body), "image_url") {
			writer.WriteHeader(rejectStatus)
			return
		}
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"seen"}}]}`))
	}))
	t.Cleanup(server.Close)
	providerID, _ := identity.ParseProviderID("openai-compatible")
	client, err := New(Config{
		Endpoint: server.URL, APIKey: "secret", ProviderID: providerID, SystemPolicy: "policy",
		Timeout: time.Second, Concurrency: 1, MaxResponseBytes: 4096, Commands: inbound.CommandRegistry(),
	})
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	request := modelRequest(t, providerID)
	request.Images = []agent.ModelImage{{Label: "Image of message 【#000001】:", Mime: "image/jpeg", Data: []byte("jpeg")}}
	return client, request, &bodies
}

func TestImagesFollowTheTranscriptAsContentParts(t *testing.T) {
	client, request, bodies := imageServer(t, 0)
	if _, err := client.Generate(context.Background(), request); err != nil {
		t.Fatalf("generate: %v", err)
	}
	var decoded struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte((*bodies)[0]), &decoded); err != nil || len(decoded.Messages) != 2 {
		t.Fatalf("request = %s, err=%v", (*bodies)[0], err)
	}
	var system string
	if err := json.Unmarshal(decoded.Messages[0].Content, &system); err != nil {
		t.Fatalf("system content is not plain text: %s", decoded.Messages[0].Content)
	}
	var parts []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	if err := json.Unmarshal(decoded.Messages[1].Content, &parts); err != nil || len(parts) != 3 {
		t.Fatalf("user content = %s, err=%v", decoded.Messages[1].Content, err)
	}
	if parts[0].Type != "text" || !strings.Contains(parts[0].Text, "hello from user") ||
		parts[1].Text != "Image of message 【#000001】:" ||
		parts[2].Type != "image_url" || parts[2].ImageURL.URL != "data:image/jpeg;base64,anBlZw==" {
		t.Fatalf("parts = %#v", parts)
	}
}

func TestImagesAreDroppedWhenTheProviderRejectsThem(t *testing.T) {
	client, request, bodies := imageServer(t, http.StatusBadRequest)
	result, err := client.Generate(context.Background(), request)
	if err != nil || result.Text != "seen" {
		t.Fatalf("generate = %#v, err=%v", result, err)
	}
	if len(*bodies) != 2 || strings.Contains((*bodies)[1], "image_url") || !strings.Contains((*bodies)[1], "hello from user") {
		t.Fatalf("requests = %q", *bodies)
	}
}

func TestImagesAreNotRetriedOnOtherFailures(t *testing.T) {
	client, request, bodies := imageServer(t, http.StatusTooManyRequests)
	if _, err := client.Generate(context.Background(), request); !agent.IsCode(err, agent.ErrorRateLimited) || len(*bodies) != 1 {
		t.Fatalf("err = %v, requests = %d", err, len(*bodies))
	}
}
