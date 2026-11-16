package directinvoke_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/directinvoke"
)

type recorder struct {
	address, prompt string
	calls           int
	err             error
}

func (r *recorder) DirectInvoke(_ context.Context, address, prompt string) error {
	r.calls++
	r.address, r.prompt = address, prompt
	return r.err
}

func serve(t *testing.T, rec *recorder, request *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	handler := directinvoke.Handler(directinvoke.Config{APIKey: "secret", MaxPromptBytes: 20, Invoker: rec})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestNoKeyMeansNoHandler(t *testing.T) {
	if directinvoke.Handler(directinvoke.Config{Invoker: &recorder{}}) != nil {
		t.Fatal("handler exists without an API key")
	}
}

func TestAcceptsAnAuthenticatedRequest(t *testing.T) {
	rec := &recorder{}
	query := url.Values{"q": {"say hi"}, "jid": {"+62 812-3456"}, "key": {"secret"}}
	response := serve(t, rec, httptest.NewRequest(http.MethodGet, "/post?"+query.Encode(), nil))
	if response.Code != http.StatusAccepted || rec.address != "628123456@s.whatsapp.net" || rec.prompt != "say hi" {
		t.Fatalf("code=%d address=%q prompt=%q", response.Code, rec.address, rec.prompt)
	}
}

func TestKeyFromHeadersAndJSONBody(t *testing.T) {
	rec := &recorder{}
	request := httptest.NewRequest(http.MethodPost, "/post", strings.NewReader(`{"q":"hello","jid":"1203@g.us"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer secret")
	if response := serve(t, rec, request); response.Code != http.StatusAccepted || rec.address != "1203@g.us" {
		t.Fatalf("bearer: code=%d address=%q", response.Code, rec.address)
	}
	request = httptest.NewRequest(http.MethodGet, "/post?q=a&jid=1203@g.us", nil)
	request.Header.Set("X-Api-Key", "secret")
	if response := serve(t, rec, request); response.Code != http.StatusAccepted {
		t.Fatalf("x-api-key: code=%d", response.Code)
	}
}

func TestRejectsBadRequests(t *testing.T) {
	cases := []struct {
		name, target string
		want         int
	}{
		{"wrong key", "/post?q=a&jid=1@g.us&key=nope", http.StatusUnauthorized},
		{"no key", "/post?q=a&jid=1@g.us", http.StatusUnauthorized},
		{"missing q", "/post?jid=1@g.us&key=secret", http.StatusBadRequest},
		{"too long", "/post?q=" + strings.Repeat("a", 21) + "&jid=1@g.us&key=secret", http.StatusRequestEntityTooLarge},
		{"bad jid", "/post?q=a&jid=hello&key=secret", http.StatusBadRequest},
	}
	for _, test := range cases {
		rec := &recorder{}
		if response := serve(t, rec, httptest.NewRequest(http.MethodGet, test.target, nil)); response.Code != test.want || rec.calls != 0 {
			t.Errorf("%s: code=%d calls=%d, want %d and no call", test.name, response.Code, rec.calls, test.want)
		}
	}
}

func TestMapsInvokerErrors(t *testing.T) {
	cases := map[agent.ErrorCode]int{
		agent.ErrorNotFound: http.StatusNotFound, agent.ErrorNotReady: http.StatusServiceUnavailable,
		agent.ErrorInvalidArgument: http.StatusBadRequest, agent.ErrorStorageFailure: http.StatusInternalServerError,
	}
	for code, want := range cases {
		rec := &recorder{err: agent.NewError(code, "test", errors.New("x"))}
		if response := serve(t, rec, httptest.NewRequest(http.MethodGet, "/post?q=a&jid=1@g.us&key=secret", nil)); response.Code != want {
			t.Errorf("%s: code=%d, want %d", code, response.Code, want)
		}
	}
}

func TestNormalizeAddress(t *testing.T) {
	for raw, want := range map[string]string{
		"123@g.us": "123@g.us", "(555) 010-2030": "5550102030@s.whatsapp.net", "x@lid": "x@lid",
	} {
		if got, ok := directinvoke.NormalizeAddress(raw); !ok || got != want {
			t.Errorf("%q = %q,%v want %q", raw, got, ok, want)
		}
	}
	for _, raw := range []string{"", "  ", "abc", "@g.us", "+"} {
		if _, ok := directinvoke.NormalizeAddress(raw); ok {
			t.Errorf("%q accepted", raw)
		}
	}
}
