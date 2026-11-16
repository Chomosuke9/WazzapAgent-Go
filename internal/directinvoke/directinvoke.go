// Package directinvoke is the authenticated HTTP endpoint that makes the bot
// send a message first: GET or POST /post with a prompt and a target chat.
//
//	/post?q=<prompt>&jid=<chat address>&key=<api key>
//
// The key may instead be sent as "X-Api-Key: <key>" or "Authorization: Bearer
// <key>", which keeps it out of URLs and proxy logs; POST callers may send q,
// jid, and key in a JSON or form body (the query string wins). jid is a
// WhatsApp address (...@g.us, ...@s.whatsapp.net) or a bare phone number.
//
// The endpoint can make the bot say anything, so it is fail-closed: with no
// API key there is no handler, every key is compared in constant time, and
// the server binds loopback unless told otherwise. A good request is answered
// 202 Accepted at once; the model runs in the background.
package directinvoke

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
)

const maxBodyBytes = 1 << 20

// Invoker starts the turn. It must not block on the model.
type Invoker interface {
	DirectInvoke(ctx context.Context, address, prompt string) error
}

// Config configures Handler.
type Config struct {
	APIKey string
	// MaxPromptBytes bounds q; a longer one is answered 413.
	MaxPromptBytes int
	Invoker        Invoker
}

var addressSuffixes = []string{"@g.us", "@s.whatsapp.net", "@lid", "@c.us"}

var phoneSeparators = regexp.MustCompile(`[\s\-()]`)

// NormalizeAddress returns the WhatsApp address for raw: a full address is
// kept, a phone number (digits, optionally with +, spaces, dashes, brackets)
// becomes <digits>@s.whatsapp.net, and anything else is rejected.
func NormalizeAddress(raw string) (string, bool) {
	address := strings.TrimSpace(raw)
	if address == "" {
		return "", false
	}
	for _, suffix := range addressSuffixes {
		if strings.HasSuffix(address, suffix) && len(address) > len(suffix) {
			return address, true
		}
	}
	digits := strings.TrimPrefix(phoneSeparators.ReplaceAllString(address, ""), "+")
	if digits == "" {
		return "", false
	}
	for _, character := range digits {
		if character < '0' || character > '9' {
			return "", false
		}
	}
	return digits + "@s.whatsapp.net", true
}

// Handler serves /post and /health. It returns nil when no API key is set.
func Handler(config Config) http.Handler {
	if config.APIKey == "" || config.Invoker == nil {
		return nil
	}
	server := &server{config: config}
	mux := http.NewServeMux()
	mux.HandleFunc("/post", server.post)
	mux.HandleFunc("/health", func(writer http.ResponseWriter, _ *http.Request) {
		writeJSON(writer, http.StatusOK, map[string]any{"status": "ok", "enabled": true})
	})
	return mux
}

type server struct{ config Config }

func (server *server) post(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodPost {
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]any{"status": "method_not_allowed"})
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maxBodyBytes)
	params := readParams(request)
	if !server.authorized(request, params["key"]) {
		writeJSON(writer, http.StatusUnauthorized, map[string]any{"status": "unauthorized"})
		return
	}
	prompt := params["q"]
	if strings.TrimSpace(prompt) == "" {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"status": "missing_q"})
		return
	}
	if len(prompt) > server.config.MaxPromptBytes {
		writeJSON(writer, http.StatusRequestEntityTooLarge, map[string]any{"status": "prompt_too_long", "max_bytes": server.config.MaxPromptBytes})
		return
	}
	address, ok := NormalizeAddress(params["jid"])
	if !ok {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"status": "invalid_jid"})
		return
	}
	err := server.config.Invoker.DirectInvoke(request.Context(), address, prompt)
	switch {
	case err == nil:
		writeJSON(writer, http.StatusAccepted, map[string]any{"status": "accepted", "jid": address})
	case agent.IsCode(err, agent.ErrorNotFound):
		writeJSON(writer, http.StatusNotFound, map[string]any{"status": "unknown_chat"})
	case agent.IsCode(err, agent.ErrorInvalidArgument):
		writeJSON(writer, http.StatusBadRequest, map[string]any{"status": "invalid_request"})
	case agent.IsCode(err, agent.ErrorNotReady):
		writeJSON(writer, http.StatusServiceUnavailable, map[string]any{"status": "not_ready"})
	default:
		writeJSON(writer, http.StatusInternalServerError, map[string]any{"status": "error"})
	}
}

// authorized compares the key from X-Api-Key, then Authorization: Bearer,
// then the key parameter, in constant time.
func (server *server) authorized(request *http.Request, param string) bool {
	provided := request.Header.Get("X-Api-Key")
	if provided == "" {
		if scheme, token, found := strings.Cut(request.Header.Get("Authorization"), " "); found && strings.EqualFold(scheme, "Bearer") {
			provided = strings.TrimSpace(token)
		}
	}
	if provided == "" {
		provided = param
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(server.config.APIKey)) == 1
}

// readParams collects q, jid, and key from the query string and, for a POST,
// from a JSON or form body; the query string wins.
func readParams(request *http.Request) map[string]string {
	params := map[string]string{}
	for _, name := range []string{"q", "jid", "key"} {
		if value := request.URL.Query().Get(name); value != "" {
			params[name] = value
		}
	}
	if request.Method != http.MethodPost || request.Body == nil {
		return params
	}
	contentType := strings.ToLower(request.Header.Get("Content-Type"))
	switch {
	case strings.Contains(contentType, "application/json"):
		var body map[string]any
		if json.NewDecoder(request.Body).Decode(&body) == nil {
			for _, name := range []string{"q", "jid", "key"} {
				if value, ok := body[name].(string); ok && params[name] == "" {
					params[name] = value
				}
			}
		}
	case strings.Contains(contentType, "application/x-www-form-urlencoded"):
		if request.ParseForm() == nil {
			for _, name := range []string{"q", "jid", "key"} {
				if value := request.PostForm.Get(name); value != "" && params[name] == "" {
					params[name] = value
				}
			}
		}
	}
	return params
}

func writeJSON(writer http.ResponseWriter, status int, body map[string]any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(body)
}
