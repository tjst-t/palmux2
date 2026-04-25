// Package auth provides Cookie + Bearer token authentication.
//
// Two modes:
//
//   - Open (--token empty): every request is allowed. The middleware just
//     ensures the browser has a palmux_session cookie so subsequent requests
//     look identical to the protected mode.
//   - Token (--token xxx): requests must carry a valid signed cookie or a
//     Bearer header matching the configured token. The /auth?token=xxx
//     endpoint exchanges the raw token for the signed cookie.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
)

// CookieName is the name of the auth cookie.
const CookieName = "palmux_session"

// CookieMaxAge is 90 days, per spec.
const CookieMaxAge = 60 * 60 * 24 * 90

// Authenticator carries the configuration and signing key.
type Authenticator struct {
	token string
	key   []byte
}

// New builds an Authenticator. token may be empty for open access.
func New(token string) (*Authenticator, error) {
	a := &Authenticator{token: token}
	// Signing key: token bytes for protected mode (so a leaked cookie value
	// is bound to the token), random bytes for open mode (just to prevent
	// trivial cookie forgery from another origin).
	if token != "" {
		a.key = []byte(token)
	} else {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		a.key = key
	}
	return a, nil
}

// Open reports whether the server is in open-access mode.
func (a *Authenticator) Open() bool { return a.token == "" }

func (a *Authenticator) sign() string {
	mac := hmac.New(sha256.New, a.key)
	mac.Write([]byte("palmux/v2"))
	return hex.EncodeToString(mac.Sum(nil))
}

// Cookie returns a freshly-signed session cookie.
func (a *Authenticator) Cookie() *http.Cookie {
	return &http.Cookie{
		Name:     CookieName,
		Value:    a.sign(),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   CookieMaxAge,
	}
}

// CheckCookie verifies the cookie's signature.
func (a *Authenticator) CheckCookie(r *http.Request) bool {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return false
	}
	expected := a.sign()
	return hmac.Equal([]byte(c.Value), []byte(expected))
}

// CheckBearer verifies a Bearer token (used by external Hooks for the
// notifications API).
func (a *Authenticator) CheckBearer(r *http.Request) bool {
	if a.Open() {
		return true
	}
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return false
	}
	return hmac.Equal([]byte(strings.TrimPrefix(h, prefix)), []byte(a.token))
}

// Middleware enforces authentication for /api/* and similar routes. In open
// mode it only ensures a cookie exists.
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.Open() {
			if _, err := r.Cookie(CookieName); err != nil {
				http.SetCookie(w, a.Cookie())
			}
			next.ServeHTTP(w, r)
			return
		}
		if a.CheckCookie(r) || a.CheckBearer(r) {
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
}

// AuthHandler implements GET /auth?token=xxx — exchange a raw token for the
// signed cookie and redirect to /.
func (a *Authenticator) AuthHandler(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if !a.Open() && !hmac.Equal([]byte(token), []byte(a.token)) {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}
	http.SetCookie(w, a.Cookie())
	http.Redirect(w, r, "/", http.StatusFound)
}
