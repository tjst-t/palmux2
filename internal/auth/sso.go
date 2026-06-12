// Package auth — SSO provider (Sbe4eee).
//
// palmux acts as the single sign-on authority for the apex (palmux itself) and
// every published container-port subdomain. Caddy delegates auth to palmux via
// forward_auth → GET /auth/verify. A successful /auth/login issues an HMAC-
// signed cookie scoped to the parent domain (Domain=.<base>), so one login
// covers the apex and all subdomains.
//
// This is NOT OIDC/OAuth and stays single-user: the password is the existing
// basic-auth credential (BASIC_AUTH_HASH, bcrypt). The signing key is derived
// from a stable secret so palmux restarts/redeploys do not log the user out.
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// SSOCookieName is the parent-domain SSO cookie.
const SSOCookieName = "palmux_sso"

const (
	ssoRememberTTL = 60 * 60 * 24 * 365 // 365d for "remember me"
	ssoSessionTTL  = 60 * 60 * 12       // 12h server-side validity for session cookies
)

// SSOProvider issues + verifies the SSO cookie and serves the login flow.
type SSOProvider struct {
	enabled        bool
	baseDomain     string // e.g. palmux-deploy-test.tjstkm.net
	basicHash      string // bcrypt hash of the password (BASIC_AUTH_HASH)
	key            []byte // HMAC signing key (stable across restarts)
	palmuxUpstream string // where Caddy reaches palmux for /auth/verify, e.g. 127.0.0.1:8080
}

// NewSSOProvider builds the provider. It is disabled (no-op) when baseDomain or
// the password hash is empty (local dev). secret pins the signing key; when
// empty it is derived from the password hash so the key is still stable.
func NewSSOProvider(baseDomain, basicHash, secret, palmuxUpstream string) *SSOProvider {
	p := &SSOProvider{
		baseDomain:     strings.TrimPrefix(baseDomain, "."),
		basicHash:      basicHash,
		palmuxUpstream: palmuxUpstream,
	}
	p.enabled = p.baseDomain != "" && basicHash != ""
	keySrc := secret
	if keySrc == "" {
		keySrc = "palmux-sso/v1/" + basicHash
	}
	sum := sha256.Sum256([]byte(keySrc))
	p.key = sum[:]
	return p
}

// Enabled reports whether SSO is configured.
func (p *SSOProvider) Enabled() bool { return p.enabled }

// BaseDomain returns the configured public base domain.
func (p *SSOProvider) BaseDomain() string { return p.baseDomain }

// CheckPassword compares pw against the bcrypt hash in constant time.
func (p *SSOProvider) CheckPassword(pw string) bool {
	if p.basicHash == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(p.basicHash), []byte(pw)) == nil
}

// signValue builds a signed cookie value: base64url(payload).hexsig where
// payload = "v1|<expUnix>|<remember 0/1>".
func (p *SSOProvider) signValue(exp int64, remember bool) string {
	rb := "0"
	if remember {
		rb = "1"
	}
	payload := fmt.Sprintf("v1|%d|%s", exp, rb)
	mac := hmac.New(sha256.New, p.key)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + hex.EncodeToString(mac.Sum(nil))
}

// parseValue validates the signature and returns (exp, remember, ok).
func (p *SSOProvider) parseValue(v string) (exp int64, remember, ok bool) {
	dot := strings.LastIndexByte(v, '.')
	if dot < 0 {
		return 0, false, false
	}
	payloadB, err := base64.RawURLEncoding.DecodeString(v[:dot])
	if err != nil {
		return 0, false, false
	}
	mac := hmac.New(sha256.New, p.key)
	mac.Write(payloadB)
	expect := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(v[dot+1:]), []byte(expect)) {
		return 0, false, false
	}
	f := strings.Split(string(payloadB), "|")
	if len(f) != 3 || f[0] != "v1" {
		return 0, false, false
	}
	exp, err = strconv.ParseInt(f[1], 10, 64)
	if err != nil {
		return 0, false, false
	}
	return exp, f[2] == "1", true
}

// cookie builds the SSO cookie for the given signed value and remember flag.
func (p *SSOProvider) cookie(value string, remember bool) *http.Cookie {
	c := &http.Cookie{
		Name:     SSOCookieName,
		Value:    value,
		Path:     "/",
		Domain:   "." + p.baseDomain,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	}
	if remember {
		c.MaxAge = ssoRememberTTL // persistent
	}
	// remember=false → MaxAge 0 → no Max-Age attribute → session cookie
	return c
}

// issueFor returns a fresh signed cookie for a successful auth.
func (p *SSOProvider) issueFor(remember bool, now time.Time) *http.Cookie {
	ttl := int64(ssoSessionTTL)
	if remember {
		ttl = ssoRememberTTL
	}
	return p.cookie(p.signValue(now.Unix()+ttl, remember), remember)
}

// verify checks the request's SSO cookie. Returns (remember, ok).
func (p *SSOProvider) verify(r *http.Request, now time.Time) (remember, ok bool) {
	c, err := r.Cookie(SSOCookieName)
	if err != nil {
		return false, false
	}
	exp, rem, valid := p.parseValue(c.Value)
	if !valid || exp < now.Unix() {
		return false, false
	}
	return rem, true
}

// safeRD validates a return-destination URL against open-redirects: it must be
// https and on the base domain (apex or a subdomain). Otherwise the apex root.
func (p *SSOProvider) safeRD(rd string) string {
	def := "https://" + p.baseDomain + "/"
	if rd == "" {
		return def
	}
	u, err := url.Parse(rd)
	if err != nil || u.Scheme != "https" {
		return def
	}
	h := u.Hostname()
	if h == p.baseDomain || strings.HasSuffix(h, "."+p.baseDomain) {
		return rd
	}
	return def
}

// ─── HTTP handlers ───────────────────────────────────────────────────────────

// LoginPage handles GET /auth/login — render the password form.
func (p *SSOProvider) LoginPage(w http.ResponseWriter, r *http.Request) {
	if !p.enabled {
		http.NotFound(w, r)
		return
	}
	p.renderLogin(w, p.safeRD(r.URL.Query().Get("rd")), "", http.StatusOK)
}

// LoginSubmit handles POST /auth/login — verify the password, set the cookie.
func (p *SSOProvider) LoginSubmit(w http.ResponseWriter, r *http.Request) {
	if !p.enabled {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	rd := p.safeRD(r.FormValue("rd"))
	remember := r.FormValue("remember") != ""
	if !p.CheckPassword(r.FormValue("password")) {
		// Re-render the form with an inline error. 401 status so automated
		// clients can distinguish, browsers still render the body.
		p.renderLogin(w, rd, "パスワードが違います。もう一度お試しください。", http.StatusUnauthorized)
		return
	}
	http.SetCookie(w, p.issueFor(remember, time.Now()))
	http.Redirect(w, r, rd, http.StatusFound)
}

// Verify handles GET /auth/verify — the Caddy forward_auth subrequest. 200 when
// authenticated (Caddy proceeds to the backend); 302 → login otherwise (Caddy
// copies the redirect to the browser). On success it re-issues the cookie so an
// actively-used client never expires (sliding renewal).
func (p *SSOProvider) Verify(w http.ResponseWriter, r *http.Request) {
	if !p.enabled {
		http.NotFound(w, r)
		return
	}
	remember, ok := p.verify(r, time.Now())
	if !ok {
		http.Redirect(w, r, p.loginURLForRequest(r), http.StatusFound)
		return
	}
	// Sliding renewal: extend the cookie on every authenticated request.
	http.SetCookie(w, p.issueFor(remember, time.Now()))
	w.WriteHeader(http.StatusOK)
}

// Logout handles GET /auth/logout — clear the cookie everywhere.
func (p *SSOProvider) Logout(w http.ResponseWriter, r *http.Request) {
	if !p.enabled {
		http.NotFound(w, r)
		return
	}
	del := p.cookie("", false)
	del.MaxAge = -1 // delete now
	http.SetCookie(w, del)
	http.Redirect(w, r, "https://"+p.baseDomain+"/auth/login", http.StatusFound)
}

// loginURLForRequest reconstructs the original request URL (from the forward_auth
// X-Forwarded-* headers) and builds the login URL that returns there.
func (p *SSOProvider) loginURLForRequest(r *http.Request) string {
	proto := firstNonEmpty(r.Header.Get("X-Forwarded-Proto"), "https")
	host := firstNonEmpty(r.Header.Get("X-Forwarded-Host"), r.Host)
	uri := firstNonEmpty(r.Header.Get("X-Forwarded-Uri"), "/")
	rd := proto + "://" + host + uri
	return "https://" + p.baseDomain + "/auth/login?rd=" + url.QueryEscape(rd)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

var loginTmpl = template.Must(template.New("login").Parse(loginHTML))

func (p *SSOProvider) renderLogin(w http.ResponseWriter, rd, errMsg string, status int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = loginTmpl.Execute(w, map[string]any{
		"RD":     rd,
		"Error":  errMsg,
		"Domain": p.baseDomain,
	})
}
