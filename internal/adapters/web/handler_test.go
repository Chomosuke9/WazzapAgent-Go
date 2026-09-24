package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestBrowserHandlerServesUIAndTypedCalls(t *testing.T) {
	handler, err := NewHandler(NewAppService("test", nil, nil, nil, nil, "", nil), fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<h1>WazzapAgent</h1>")},
	})
	if err != nil {
		t.Fatal(err)
	}
	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/", nil))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "WazzapAgent") {
		t.Fatalf("unexpected page: %d %q", page.Code, page.Body.String())
	}
	for _, method := range []string{"GetAppInfo", "Ping"} {
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
	handler, err := NewHandler(NewAppService("test", nil, nil, nil, nil, "", nil), fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}})
	if err != nil {
		t.Fatal(err)
	}
	if response := callForTest(handler, "Ping", "https://other.example", "127.0.0.1:8080"); response.Code != http.StatusForbidden {
		t.Fatalf("cross-site status = %d", response.Code)
	}
	if response := callForTest(handler, "Ping", "http://other.example", "other.example"); response.Code != http.StatusForbidden {
		t.Fatalf("non-loopback host status = %d", response.Code)
	}
	if response := callForTest(handler, "GetSettingsSchemaHidden", "http://127.0.0.1:8080", "127.0.0.1:8080"); response.Code != http.StatusBadRequest {
		t.Fatalf("unknown operation status = %d", response.Code)
	}
}

func TestBrowserHandlerAllowsConfiguredHTTPSProxyOrigin(t *testing.T) {
	handler, err := NewHandler(NewAppService("test", nil, nil, nil, nil, "", nil), fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, "https://panel.example")
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
