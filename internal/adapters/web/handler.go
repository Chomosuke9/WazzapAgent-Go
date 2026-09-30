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
	"strconv"
	"strings"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/ui"
)

type request struct {
	Method string            `json:"method"`
	Args   []json.RawMessage `json:"args"`
}

type response struct {
	Result any    `json:"result"`
	Error  string `json:"error,omitempty"`
}

// Options configures the browser handler.
type Options struct {
	// PublicOrigin is the HTTPS origin a reverse proxy serves the UI from.
	PublicOrigin string
	// Auth, when set, requires a login for every call and lets the UI be
	// reached under any Host name. Without it only loopback hosts and
	// PublicOrigin are served, so the caller must keep the listener private.
	Auth *TokenAuth
}

type authStatus struct {
	Required      bool `json:"required"`
	Authenticated bool `json:"authenticated"`
}

type loginRequest struct {
	Token string `json:"token"`
}

// NewHandler serves the UI without a login; see NewHandlerWithOptions.
func NewHandler(service *ui.AppService, assets fs.FS, publicOrigin ...string) (http.Handler, error) {
	options := Options{}
	if len(publicOrigin) > 0 {
		options.PublicOrigin = publicOrigin[0]
	}
	return NewHandlerWithOptions(service, assets, options)
}

func NewHandlerWithOptions(service *ui.AppService, assets fs.FS, options Options) (http.Handler, error) {
	if service == nil || assets == nil {
		return nil, errors.New("web service and assets are required")
	}
	auth := options.Auth
	trustedHost := ""
	if options.PublicOrigin != "" {
		u, err := url.Parse(options.PublicOrigin)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
			return nil, errors.New("public web origin must be an HTTPS origin without a path")
		}
		trustedHost = u.Host
	}
	static := http.FileServer(http.FS(assets))
	mux := http.NewServeMux()
	allowed := func(r *http.Request) bool {
		return allowedBrowserRequest(r, trustedHost, auth != nil)
	}
	// secure marks the cookie Secure only where the browser reached us over
	// HTTPS; a Secure cookie set over plain HTTP would never be stored.
	secure := func(r *http.Request) bool {
		return r.TLS != nil || trustedHost != "" && strings.EqualFold(r.Host, trustedHost)
	}
	mux.HandleFunc("GET /api/auth/status", func(w http.ResponseWriter, r *http.Request) {
		if !allowed(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		writeJSON(w, http.StatusOK, authStatus{Required: auth != nil, Authenticated: auth == nil || auth.Authenticated(r)})
	})
	mux.HandleFunc("POST /api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		if !allowed(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if auth == nil {
			writeJSON(w, http.StatusOK, authStatus{})
			return
		}
		var input loginRequest
		if !decodeJSONBody(w, r, &input) {
			return
		}
		cookie, retryAfter, ok := auth.Login(r, strings.TrimSpace(input.Token), secure(r))
		if retryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())+1))
			writeJSON(w, http.StatusTooManyRequests, response{Error: "Too many failed attempts. Try again later."})
			return
		}
		if !ok {
			writeJSON(w, http.StatusUnauthorized, response{Error: "The access token is not correct."})
			return
		}
		http.SetCookie(w, cookie)
		writeJSON(w, http.StatusOK, authStatus{Required: true, Authenticated: true})
	})
	mux.HandleFunc("POST /api/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		if !allowed(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		http.SetCookie(w, expiredSessionCookie(secure(r)))
		writeJSON(w, http.StatusOK, authStatus{Required: auth != nil})
	})
	mux.HandleFunc("POST /api/call", func(w http.ResponseWriter, r *http.Request) {
		if !allowed(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if auth != nil && !auth.Authenticated(r) {
			writeJSON(w, http.StatusUnauthorized, response{Error: "Sign in with the access token."})
			return
		}
		var input request
		if !decodeJSONBody(w, r, &input) {
			return
		}
		result, err := invoke(service, input)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, response{Error: publicError(err)})
			return
		}
		writeJSON(w, http.StatusOK, response{Result: result})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if auth == nil && !allowedHost(r.Host, trustedHost) {
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

// decodeJSONBody reads exactly one JSON object from a same-origin request and
// writes the error response itself when the body is not acceptable.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, into any) bool {
	if r.Header.Get("Content-Type") != "application/json" {
		http.Error(w, "application/json required", http.StatusUnsupportedMediaType)
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		http.Error(w, "request must contain one JSON object", http.StatusBadRequest)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
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

// allowedBrowserRequest keeps requests same-origin. With anyHost (a login is
// required) the Host name is not restricted: a rebinding page never holds the
// session cookie, which is scoped to the real host name.
func allowedBrowserRequest(r *http.Request, trustedHost string, anyHost bool) bool {
	if !anyHost && !allowedHost(r.Host, trustedHost) || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
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

func invoke(service *ui.AppService, input request) (any, error) {
	// Every exported ui.AppService method is a UI operation; Wails exposes the
	// same set. Reflection only decodes the typed arguments.
	method := reflect.ValueOf(service).MethodByName(input.Method)
	if !method.IsValid() {
		return nil, errors.New("unknown operation")
	}
	if method.Type().NumIn() != len(input.Args) {
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
