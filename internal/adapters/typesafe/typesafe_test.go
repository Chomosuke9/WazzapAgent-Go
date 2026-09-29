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
	client, err := New("test-key", server.URL+"/v1/systemone", "", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestJudgeAsksEachQuestionAndSendsTranscript(t *testing.T) {
	var body struct {
		Model     string              `json:"model"`
		State     map[string]any      `json:"state"`
		Questions map[string]Question `json:"questions"`
	}
	yes := map[string]float64{"followup": 0.9}
	client := testClient(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/systemone" || request.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("request = %s %s auth %q", request.Method, request.URL.Path, request.Header.Get("Authorization"))
		}
		raw, _ := io.ReadAll(request.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		answers := map[string]Answer{}
		for name := range body.Questions {
			answers[name] = Answer{Type: "noul", Noul: yes[name]}
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"model": "jev-1", "answers": answers})
	})
	current, _ := identity.NewMessageID()
	history := fakeHistory{entries: []agent.HistoryEntry{
		{Sequence: 2, Role: agent.HistoryAssistant, Content: []agent.ContentPart{agent.TextPart{Text: "Mau ukuran berapa?"}}},
		{Sequence: 3, MessageID: current, Role: agent.HistoryUser, Content: []agent.ContentPart{agent.TextPart{Text: "yang besar"}}},
		{Sequence: 1, Role: agent.HistoryUser, Sender: &agent.SenderContext{DisplayName: "Budi"}, Content: []agent.ContentPart{agent.TextPart{Text: "Vivy, pesan kopi"}}},
	}}
	judge := NewResponseJudge(client, history, "Vivy", slog.New(slog.DiscardHandler))
	message := conversation.IncomingMessage{ID: current, SenderName: "Budi", Text: "yang besar", ChatKind: conversation.ChatGroup}
	snapshot := agent.ConfigSnapshot{Version: 1, Triggers: agent.TriggerConfig{Smart: true, SmartRules: "someone sends a scam link\n\n  someone asks about prices  "}}

	respond, err := judge.ShouldRespond(t.Context(), message, snapshot)
	if err != nil || !respond {
		t.Fatalf("respond = %v, %v; want true for a follow-up", respond, err)
	}
	if body.Model != DefaultModel || len(body.Questions) != 6 || body.Questions["rule_2"].Type != "noul" {
		t.Fatalf("request model/questions = %q %#v", body.Model, body.Questions)
	}
	// A rule case in an earlier message was handled when it arrived, so
	// the rule question is about new_message alone.
	if rule, _ := json.Marshal(body.Questions["rule_2"].Instructions); !strings.Contains(string(rule), `"rule":"someone asks about prices"`) ||
		!strings.Contains(string(rule), "Judge only `new_message`. `earlier_messages` were already judged and handled") {
		t.Fatalf("rule_2 instructions = %s", rule)
	}
	recent, _ := json.Marshal(body.State["earlier_messages"])
	if got := string(recent); got != `[{"from":"Budi","text":"Vivy, pesan kopi"},{"from":"Vivy (the assistant)","text":"Mau ukuran berapa?"}]` {
		t.Fatalf("earlier_messages = %s (want oldest first, current message excluded)", got)
	}

	yes = map[string]float64{"followup": 0.9, "chatter": 0.8}
	if respond, err := judge.ShouldRespond(t.Context(), message, snapshot); err != nil || respond {
		t.Fatalf("respond to chatter = %v, %v; want false", respond, err)
	}
	yes = map[string]float64{"to_someone_else": 0.9, "rule_1": 0.7}
	if respond, err := judge.ShouldRespond(t.Context(), message, snapshot); err != nil || !respond {
		t.Fatalf("respond to a rule match = %v, %v; want true", respond, err)
	}
}

func TestDecide(t *testing.T) {
	for _, test := range []struct {
		name string
		in   Probabilities
		want bool
	}{
		{"nothing", Probabilities{}, false},
		{"follow-up", Probabilities{FollowUp: 0.8}, true},
		{"addressed", Probabilities{Addressed: 0.8}, true},
		{"addressed but to someone else", Probabilities{Addressed: 0.8, ToSomeoneElse: 0.6}, false},
		{"follow-up but chatter", Probabilities{FollowUp: 0.8, Chatter: 0.9}, false},
		{"rule beats exclusions", Probabilities{ToSomeoneElse: 0.9, Chatter: 0.9, Rules: []float64{0.2, 0.6}}, true},
		{"exactly the threshold is no", Probabilities{FollowUp: Threshold}, false},
	} {
		if got, reason := Decide(test.in); got != test.want {
			t.Errorf("%s: Decide = %v (%s), want %v", test.name, got, reason, test.want)
		}
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

func TestNewDefaultsAndChecksTheEndpoint(t *testing.T) {
	client, err := New("key", " ", " ", nil)
	if err != nil || client.endpoint != DefaultEndpoint || client.model != DefaultModel {
		t.Fatalf("empty endpoint and model = %v, %v", client, err)
	}
	if client, err := New("key", "", " jev-2 ", nil); err != nil || client.model != "jev-2" {
		t.Fatalf("configured model = %v, %v", client, err)
	}
	if _, err := New("key", "api.typesafe.ai/v1/systemone", "", nil); err == nil {
		t.Fatal("accepted an endpoint without a scheme")
	}
}

func TestTruncateKeepsRunesWhole(t *testing.T) {
	if got := truncate("aé", 2); got != "a…" {
		t.Fatalf("truncate = %q", got)
	}
}
