package web

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strings"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
)

// The browser bridge exposes exactly the operations used by the shared UI.
// Reflection only decodes their typed arguments; it never selects an arbitrary method.
var allowedMethods = map[string]bool{
	"GetAppInfo": true, "Ping": true, "GetLogs": true,
	"GetSettings": true, "GetSettingsSchema": true, "ValidateSettings": true, "SaveSettings": true,
	"GetAgentRuntimeStatus": true, "StartAgent": true, "StopAgent": true, "ApplyAgentSettings": true,
	"GetWhatsAppSessionStatus": true, "BeginWhatsAppPairing": true, "ResumeWhatsAppSession": true,
	"StopWhatsAppSession": true, "CancelWhatsAppPairing": true, "ReconnectWhatsAppSession": true,
	"LogoutWhatsAppSession": true, "GetWhatsAppConversations": true, "GetWhatsAppMessages": true,
	"GetWhatsAppGroupMembers": true, "GetWhatsAppChatSettings": true, "SaveWhatsAppChatSettings": true,
	"ResetWhatsAppChatSettings":  true,
	"GetWhatsAppBroadcastGroups": true, "NormalizeWhatsAppBroadcastPayload": true, "SendWhatsAppBroadcast": true,
	"ScheduleWhatsAppBroadcast": true, "GetWhatsAppBroadcastSchedules": true, "CancelWhatsAppBroadcastSchedule": true,
	"SendWhatsAppMessage": true, "DeleteWhatsAppMessage": true, "KickWhatsAppGroupMember": true,
}

type request struct {
	Method string            `json:"method"`
	Args   []json.RawMessage `json:"args"`
}

type response struct {
	Result any    `json:"result"`
	Error  string `json:"error,omitempty"`
}

func NewHandler(service *AppService, assets fs.FS, publicOrigin ...string) (http.Handler, error) {
	if service == nil || assets == nil {
		return nil, errors.New("web service and assets are required")
	}
	trustedHost := ""
	if len(publicOrigin) > 0 && publicOrigin[0] != "" {
		u, err := url.Parse(publicOrigin[0])
		if err != nil || u.Scheme != "https" || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
			return nil, errors.New("public web origin must be an HTTPS origin without a path")
		}
		trustedHost = u.Host
	}
	static := http.FileServer(http.FS(assets))
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/call", func(w http.ResponseWriter, r *http.Request) {
		if !allowedBrowserRequest(r, trustedHost) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "application/json required", http.StatusUnsupportedMediaType)
			return
		}
		var input request
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			http.Error(w, "request must contain one JSON object", http.StatusBadRequest)
			return
		}
		result, err := invoke(service, input)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(response{Error: publicError(err)})
			return
		}
		_ = json.NewEncoder(w).Encode(response{Result: result})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if !allowedHost(r.Host, trustedHost) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; base-uri 'none'; object-src 'none'; frame-ancestors 'none'")
		if r.URL.Path == "/" {
			w.Header().Set("Cache-Control", "no-store")
		}
		static.ServeHTTP(w, r)
	})
	return mux, nil
}

func allowedHost(hostport, trustedHost string) bool {
	if trustedHost != "" && strings.EqualFold(hostport, trustedHost) {
		return true
	}
	host := hostport
	if parsed, _, err := net.SplitHostPort(hostport); err == nil {
		host = parsed
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func allowedBrowserRequest(r *http.Request, trustedHost string) bool {
	if !allowedHost(r.Host, trustedHost) || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || !strings.EqualFold(u.Host, r.Host) {
			return false
		}
		if trustedHost != "" && strings.EqualFold(r.Host, trustedHost) && u.Scheme != "https" {
			return false
		}
	}
	return true
}

func invoke(service *AppService, input request) (any, error) {
	if !allowedMethods[input.Method] {
		return nil, errors.New("unknown operation")
	}
	method := reflect.ValueOf(service).MethodByName(input.Method)
	if !method.IsValid() || method.Type().NumIn() != len(input.Args) {
		return nil, errors.New("invalid operation arguments")
	}
	args := make([]reflect.Value, len(input.Args))
	for i, raw := range input.Args {
		arg := reflect.New(method.Type().In(i))
		if err := json.Unmarshal(raw, arg.Interface()); err != nil {
			return nil, errors.New("invalid operation arguments")
		}
		args[i] = arg.Elem()
	}
	output := method.Call(args)
	if len(output) == 0 {
		return nil, nil
	}
	last := output[len(output)-1]
	if last.Type().Implements(reflect.TypeOf((*error)(nil)).Elem()) {
		if !last.IsNil() {
			return nil, last.Interface().(error)
		}
		if len(output) == 1 {
			return nil, nil
		}
	}
	return output[0].Interface(), nil
}

func publicError(err error) string {
	switch agent.CodeOf(err) {
	case agent.ErrorInvalidArgument, agent.ErrorNotReady, agent.ErrorConflict:
		return err.Error()
	default:
		return "The operation failed. Check the application logs."
	}
}
