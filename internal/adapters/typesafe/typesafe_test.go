package typesafe

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

type fakeHistory struct{ entries []agent.HistoryEntry }

func (history fakeHistory) ListIfConfigVersion(context.Context, agent.Key, agent.ConfigVersion, agent.HistoryQuery) (agent.HistoryPage, error) {
	return agent.HistoryPage{Entries: history.entries}, nil
}

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := New("test-key", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	client.baseURL = server.URL
	return client
}

func TestJudgeSendsTranscriptAndAppliesThreshold(t *testing.T) {
	var body struct {
		Model     string              `json:"model"`
		State     map[string]any      `json:"state"`
		Questions map[string]Question `json:"questions"`
	}
	probability := 0.8
	client := testClient(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/systemone" || request.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("request = %s %s auth %q", request.Method, request.URL.Path, request.Header.Get("Authorization"))
		}
		raw, _ := io.ReadAll(request.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"model":"jev-1","answers":{"addressed":{"type":"noul","noul":`+jsonNumber(probability)+`}},"usage":{"input_tokens":120,"output_tokens":1}}`)
	})
	current, _ := identity.NewMessageID()
	history := fakeHistory{entries: []agent.HistoryEntry{
		{Sequence: 2, Role: agent.HistoryAssistant, Content: []agent.ContentPart{agent.TextPart{Text: "Mau ukuran berapa?"}}},
		{Sequence: 3, MessageID: current, Role: agent.HistoryUser, Content: []agent.ContentPart{agent.TextPart{Text: "yang besar"}}},
		{Sequence: 1, Role: agent.HistoryUser, Sender: &agent.SenderContext{DisplayName: "Budi"}, Content: []agent.ContentPart{agent.TextPart{Text: "Vivy, pesan kopi"}}},
	}}
	judge := NewAddressJudge(client, history, "Vivy", slog.New(slog.DiscardHandler))
	message := conversation.IncomingMessage{ID: current, SenderName: "Budi", Text: "yang besar", ChatKind: conversation.ChatGroup}

	addressed, err := judge.AddressedToAssistant(t.Context(), message, agent.ConfigSnapshot{Version: 1})
	if err != nil || !addressed {
		t.Fatalf("addressed = %v, %v; want true", addressed, err)
	}
	if body.Model != DefaultModel || body.Questions["addressed"].Type != "noul" {
		t.Fatalf("request model/questions = %q %#v", body.Model, body.Questions)
	}
	recent, _ := json.Marshal(body.State["recent_messages"])
	if got := string(recent); got != `[{"from":"Budi","text":"Vivy, pesan kopi"},{"from":"Vivy (the assistant)","text":"Mau ukuran berapa?"}]` {
		t.Fatalf("recent_messages = %s (want oldest first, current message excluded)", got)
	}

	probability = 0.3
	if addressed, err := judge.AddressedToAssistant(t.Context(), message, agent.ConfigSnapshot{Version: 1}); err != nil || addressed {
		t.Fatalf("addressed at 0.3 = %v, %v; want false", addressed, err)
	}
}

func TestClientReportsHTTPErrors(t *testing.T) {
	client := testClient(t, func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("x-typesafe-request-id", "req_1")
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(writer, `{"error":"bad key"}`)
	})
	_, err := client.SystemOne(t.Context(), "text", map[string]Question{"q": {Type: "noul", Instructions: "?"}})
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") || !strings.Contains(err.Error(), "req_1") {
		t.Fatalf("error = %v", err)
	}
}

func TestClientRejectsMissingAnswer(t *testing.T) {
	client := testClient(t, func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, `{"model":"jev-1","answers":{}}`)
	})
	if _, err := client.SystemOne(t.Context(), "text", map[string]Question{"q": {Type: "noul"}}); err == nil {
		t.Fatal("accepted a response without the asked answer")
	}
}

func TestTruncateKeepsRunesWhole(t *testing.T) {
	if got := truncate("aé", 2); got != "a…" {
		t.Fatalf("truncate = %q", got)
	}
}

func jsonNumber(value float64) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}
