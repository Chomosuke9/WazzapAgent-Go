package web

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// SessionCookie is the browser cookie that remembers a successful login.
	SessionCookie = "wazzap_session"
	// SessionLifetime is how long a login stays valid without entering the token again.
	SessionLifetime = 180 * 24 * time.Hour
	// MinTokenLength is the shortest token accepted from configuration.
	MinTokenLength = 16

	loginMaxFailures = 10
	loginWindow      = 15 * time.Minute
)

// TokenAuth gates the web UI behind one shared access token. A correct token
// is exchanged for a signed session cookie, so the browser keeps the login
// without the token itself being stored anywhere in the page.
//
// The cookie is a stateless HMAC of its expiry keyed from the token: it
// survives a server restart, and rotating the token revokes every session.
type TokenAuth struct {
	tokenDigest [sha256.Size]byte
	key         []byte
	now         func() time.Time
	limiter     *loginLimiter
}

// NewTokenAuth builds an authenticator for token.
func NewTokenAuth(token string) (*TokenAuth, error) {
	if len(token) < MinTokenLength {
		return nil, fmt.Errorf("access token must be at least %d characters", MinTokenLength)
	}
	key := sha256.Sum256([]byte("wazzapagent-web-session-v1\x00" + token))
	return &TokenAuth{
		tokenDigest: sha256.Sum256([]byte(token)),
		key:         key[:],
		now:         time.Now,
		limiter:     &loginLimiter{failures: map[string]*failureWindow{}},
	}, nil
}

// GenerateToken returns a new random access token.
func GenerateToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate access token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// LoadToken reads the access token stored at path. With create set, a missing
// file is filled with a fresh token and created reports true. With rotate set,
// the stored token is replaced.
func LoadToken(path string, create, rotate bool) (token string, created bool, err error) {
	if !rotate {
		data, readErr := os.ReadFile(path)
		if readErr == nil {
			token = strings.TrimSpace(string(data))
			if len(token) < MinTokenLength {
				return "", false, fmt.Errorf("access token in %s is too short; delete the file to generate a new one", path)
			}
			return token, false, nil
		}
		if !errors.Is(readErr, os.ErrNotExist) {
			return "", false, fmt.Errorf("read access token: %w", readErr)
		}
		if !create {
			return "", false, fmt.Errorf("no access token yet at %s", path)
		}
	}
	token, err = GenerateToken()
	if err != nil {
		return "", false, err
	}
	if err := writeSecretFile(path, token+"\n"); err != nil {
		return "", false, err
	}
	return token, true, nil
}

// writeSecretFile replaces path atomically with owner-only permissions.
func writeSecretFile(path, content string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	temp, err := os.CreateTemp(dir, ".web-token-*")
	if err != nil {
		return fmt.Errorf("write access token: %w", err)
	}
	defer os.Remove(temp.Name())
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return fmt.Errorf("write access token: %w", err)
	}
	if _, err := temp.WriteString(content); err != nil {
		temp.Close()
		return fmt.Errorf("write access token: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("write access token: %w", err)
	}
	if err := os.Rename(temp.Name(), path); err != nil {
		return fmt.Errorf("write access token: %w", err)
	}
	return nil
}

func (a *TokenAuth) tokenMatches(candidate string) bool {
	digest := sha256.Sum256([]byte(candidate))
	return subtle.ConstantTimeCompare(digest[:], a.tokenDigest[:]) == 1
}

func (a *TokenAuth) sign(expiry int64) string {
	mac := hmac.New(sha256.New, a.key)
	mac.Write([]byte(strconv.FormatInt(expiry, 10)))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (a *TokenAuth) newSessionValue() (string, time.Time) {
	expires := a.now().Add(SessionLifetime)
	return strconv.FormatInt(expires.Unix(), 10) + "." + a.sign(expires.Unix()), expires
}

func (a *TokenAuth) validSession(value string) bool {
	expiryText, signature, ok := strings.Cut(value, ".")
	if !ok {
		return false
	}
	expiry, err := strconv.ParseInt(expiryText, 10, 64)
	if err != nil || a.now().Unix() >= expiry {
		return false
	}
	return hmac.Equal([]byte(signature), []byte(a.sign(expiry)))
}

// Authenticated reports whether the request carries a valid session cookie.
func (a *TokenAuth) Authenticated(r *http.Request) bool {
	cookie, err := r.Cookie(SessionCookie)
	return err == nil && a.validSession(cookie.Value)
}

// Login checks token for the caller and, when it is right, returns the cookie
// that remembers the login. retryAfter is set when the caller is blocked.
func (a *TokenAuth) Login(r *http.Request, token string, secure bool) (cookie *http.Cookie, retryAfter time.Duration, ok bool) {
	client := clientAddress(r)
	if wait := a.limiter.blockedFor(client, a.now()); wait > 0 {
		return nil, wait, false
	}
	if !a.tokenMatches(token) {
		a.limiter.recordFailure(client, a.now())
		return nil, 0, false
	}
	a.limiter.reset(client)
	value, expires := a.newSessionValue()
	return &http.Cookie{
		Name:     SessionCookie,
		Value:    value,
		Path:     "/",
		Expires:  expires,
		MaxAge:   int(SessionLifetime.Seconds()),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	}, 0, true
}

func expiredSessionCookie(secure bool) *http.Cookie {
	return &http.Cookie{
		Name:     SessionCookie,
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	}
}

func clientAddress(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// loginLimiter slows down token guessing: after loginMaxFailures wrong tokens
// from one address, that address is refused until the window passes.
type loginLimiter struct {
	mu       sync.Mutex
	failures map[string]*failureWindow
}

type failureWindow struct {
	count int
	start time.Time
}

func (l *loginLimiter) blockedFor(client string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	window := l.failures[client]
	if window == nil {
		return 0
	}
	if now.Sub(window.start) >= loginWindow {
		delete(l.failures, client)
		return 0
	}
	if window.count >= loginMaxFailures {
		return window.start.Add(loginWindow).Sub(now)
	}
	return 0
}

func (l *loginLimiter) recordFailure(client string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for address, window := range l.failures {
		if now.Sub(window.start) >= loginWindow {
			delete(l.failures, address)
		}
	}
	window := l.failures[client]
	if window == nil {
		window = &failureWindow{start: now}
		l.failures[client] = window
	}
	window.count++
}

func (l *loginLimiter) reset(client string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, client)
}
