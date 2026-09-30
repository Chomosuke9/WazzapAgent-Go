package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Chomosuke9/DiscordAgent-Go/internal/ui"
)

func TestBrowserHandlerServesUIAndTypedCalls(t *testing.T) {
	handler, err := NewHandler(ui.NewAppService(ui.Options{Version: "test"}), fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<h1>DiscordAgent</h1>")},
	})
	if err != nil {
		t.Fatal(err)
	}
	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/", nil))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "DiscordAgent") {
		t.Fatalf("unexpected page: %d %q", page.Code, page.Body.String())
	}
	for _, method := range []string{"GetAppInfo"} {
		response := callForTest(handler, method, "http://127.0.0.1:8080", "127.0.0.1:8080")
		if response.Code != http.StatusOK {
			t.Fatalf("%s: %d %q", method, response.Code, response.Body.String())
		}
		var body map[string]map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body["result"] == nil {
			t.Fatalf("%s returned no JSON result: %v", method, err)
		}
	}
}

func TestBrowserHandlerRejectsCrossSiteAndUnknownOperations(t *testing.T) {
	handler, err := NewHandler(ui.NewAppService(ui.Options{Version: "test"}), fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}})
	if err != nil {
		t.Fatal(err)
	}
	if response := callForTest(handler, "GetAppInfo", "https://other.example", "127.0.0.1:8080"); response.Code != http.StatusForbidden {
		t.Fatalf("cross-site status = %d", response.Code)
	}
	if response := callForTest(handler, "GetAppInfo", "http://other.example", "other.example"); response.Code != http.StatusForbidden {
		t.Fatalf("non-loopback host status = %d", response.Code)
	}
	if response := callForTest(handler, "GetSettingsSchemaHidden", "http://127.0.0.1:8080", "127.0.0.1:8080"); response.Code != http.StatusBadRequest {
		t.Fatalf("unknown operation status = %d", response.Code)
	}
}

func TestBrowserHandlerAllowsConfiguredHTTPSProxyOrigin(t *testing.T) {
	handler, err := NewHandler(ui.NewAppService(ui.Options{Version: "test"}), fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, "https://panel.example")
	if err != nil {
		t.Fatal(err)
	}
	if response := callForTest(handler, "GetAppInfo", "https://panel.example", "panel.example"); response.Code != http.StatusOK {
		t.Fatalf("proxy origin status = %d: %s", response.Code, response.Body.String())
	}
	if response := callForTest(handler, "GetAppInfo", "http://panel.example", "panel.example"); response.Code != http.StatusForbidden {
		t.Fatalf("insecure proxy origin status = %d", response.Code)
	}
}

func callForTest(handler http.Handler, method, origin, host string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/api/call", io.NopCloser(strings.NewReader(`{"method":"`+method+`","args":[]}`)))
	request.Host = host
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", origin)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func authedHandler(t *testing.T, token string) http.Handler {
	t.Helper()
	auth, err := NewTokenAuth(token)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandlerWithOptions(ui.NewAppService(ui.Options{Version: "test"}), fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, Options{Auth: auth})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func postForTest(handler http.Handler, path, body, host, remote string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "http://"+host+path, strings.NewReader(body))
	request.Host = host
	request.RemoteAddr = remote
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://"+host)
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestTokenLoginIssuesRememberedSession(t *testing.T) {
	const token = "correct-horse-battery-staple"
	handler := authedHandler(t, token)
	const host = "192.168.1.20:8080"
	call := `{"method":"GetAppInfo","args":[]}`

	if response := postForTest(handler, "/api/call", call, host, "10.0.0.1:1"); response.Code != http.StatusUnauthorized {
		t.Fatalf("call without login = %d", response.Code)
	}
	if response := postForTest(handler, "/api/auth/login", `{"token":"wrong-token-value-1"}`, host, "10.0.0.1:1"); response.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token = %d", response.Code)
	}
	login := postForTest(handler, "/api/auth/login", `{"token":"`+token+`"}`, host, "10.0.0.1:1")
	if login.Code != http.StatusOK {
		t.Fatalf("login = %d: %s", login.Code, login.Body.String())
	}
	cookies := login.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != SessionCookie || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode || cookies[0].Secure || cookies[0].MaxAge <= 0 {
		t.Fatalf("unexpected session cookie: %+v", cookies)
	}
	if strings.Contains(cookies[0].Value, token) {
		t.Fatal("session cookie leaks the token")
	}
	if response := postForTest(handler, "/api/call", call, host, "10.0.0.1:1", cookies[0]); response.Code != http.StatusOK {
		t.Fatalf("call with session = %d: %s", response.Code, response.Body.String())
	}
	if response := postForTest(handler, "/api/call", call, host, "10.0.0.1:1", &http.Cookie{Name: SessionCookie, Value: cookies[0].Value + "x"}); response.Code != http.StatusUnauthorized {
		t.Fatalf("tampered session = %d", response.Code)
	}

	// The same session must survive a restart, and a rotated token must not.
	if response := postForTest(authedHandler(t, token), "/api/call", call, host, "10.0.0.1:1", cookies[0]); response.Code != http.StatusOK {
		t.Fatalf("session after restart = %d", response.Code)
	}
	if response := postForTest(authedHandler(t, "another-rotated-token-1"), "/api/call", call, host, "10.0.0.1:1", cookies[0]); response.Code != http.StatusUnauthorized {
		t.Fatalf("session after rotation = %d", response.Code)
	}
}

func TestSessionExpiresAndSecureFollowsTransport(t *testing.T) {
	auth, err := NewTokenAuth("correct-horse-battery-staple")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	auth.now = func() time.Time { return now }
	value, _ := auth.newSessionValue()
	if !auth.validSession(value) {
		t.Fatal("fresh session rejected")
	}
	now = now.Add(SessionLifetime + time.Second)
	if auth.validSession(value) {
		t.Fatal("expired session accepted")
	}

	handler, err := NewHandlerWithOptions(ui.NewAppService(ui.Options{Version: "test"}), fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, Options{Auth: auth, PublicOrigin: "https://panel.example"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "https://panel.example/api/auth/login", strings.NewReader(`{"token":"correct-horse-battery-staple"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://panel.example")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if cookies := response.Result().Cookies(); response.Code != http.StatusOK || len(cookies) != 1 || !cookies[0].Secure {
		t.Fatalf("HTTPS login = %d cookies=%+v", response.Code, cookies)
	}
}

func TestLoginIsRateLimitedPerClient(t *testing.T) {
	handler := authedHandler(t, "correct-horse-battery-staple")
	const host = "192.168.1.20:8080"
	for i := 0; i < loginMaxFailures; i++ {
		if response := postForTest(handler, "/api/auth/login", `{"token":"wrong"}`, host, "10.0.0.9:1"); response.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d", i, response.Code)
		}
	}
	blocked := postForTest(handler, "/api/auth/login", `{"token":"correct-horse-battery-staple"}`, host, "10.0.0.9:2")
	if blocked.Code != http.StatusTooManyRequests || blocked.Header().Get("Retry-After") == "" {
		t.Fatalf("blocked login = %d", blocked.Code)
	}
	if response := postForTest(handler, "/api/auth/login", `{"token":"correct-horse-battery-staple"}`, host, "10.0.0.10:1"); response.Code != http.StatusOK {
		t.Fatalf("other client = %d", response.Code)
	}
}

func TestAuthenticatedHandlerStillRejectsCrossSiteRequests(t *testing.T) {
	handler := authedHandler(t, "correct-horse-battery-staple")
	request := httptest.NewRequest(http.MethodPost, "http://192.168.1.20:8080/api/auth/login", strings.NewReader(`{"token":"correct-horse-battery-staple"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://evil.example")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-site login = %d", response.Code)
	}
}

func TestAuthStatusWithoutTokenAuthIsOpen(t *testing.T) {
	handler, err := NewHandler(ui.NewAppService(ui.Options{Version: "test"}), fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/api/auth/status", nil))
	if !strings.Contains(response.Body.String(), `"required":false`) || !strings.Contains(response.Body.String(), `"authenticated":true`) {
		t.Fatalf("status = %s", response.Body.String())
	}
}

func TestLoadTokenPersistsAndRotates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "web-token")
	if _, _, err := LoadToken(path, false, false); err == nil {
		t.Fatal("missing token without create should fail")
	}
	first, created, err := LoadToken(path, true, false)
	if err != nil || !created || len(first) < MinTokenLength {
		t.Fatalf("create: %q %v %v", first, created, err)
	}
	again, created, err := LoadToken(path, true, false)
	if err != nil || created || again != first {
		t.Fatalf("reload: %q %v %v", again, created, err)
	}
	rotated, created, err := LoadToken(path, true, true)
	if err != nil || !created || rotated == first {
		t.Fatalf("rotate: %q %v %v", rotated, created, err)
	}
	if info, err := os.Stat(path); err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode: %v %v", info, err)
	}
}

func TestConfiguredTokenWhitespaceIsIgnored(t *testing.T) {
	handler := authedHandler(t, "correct-horse-battery-staple\n")
	response := postForTest(handler, "/api/auth/login", `{"token":"correct-horse-battery-staple"}`, "192.168.1.20:8080", "10.0.0.1:1")
	if response.Code != http.StatusOK {
		t.Fatalf("login with newline-terminated configured token = %d", response.Code)
	}
}

func TestParallelLoginAttemptsCannotBypassLimit(t *testing.T) {
	handler := authedHandler(t, "correct-horse-battery-staple")
	const attempts = 60
	codes := make(chan int, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes <- postForTest(handler, "/api/auth/login", `{"token":"wrong"}`, "192.168.1.20:8080", "10.0.0.9:1").Code
		}()
	}
	wg.Wait()
	close(codes)
	checked := 0
	for code := range codes {
		if code == http.StatusUnauthorized {
			checked++
		}
	}
	if checked != loginMaxFailures {
		t.Fatalf("%d tokens were checked from one address, want %d", checked, loginMaxFailures)
	}
}
