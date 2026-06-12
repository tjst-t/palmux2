package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func testHash(t *testing.T, pw string) string {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	return string(h)
}

func TestSSOEnabled(t *testing.T) {
	if NewSSOProvider("", "", "", "127.0.0.1:8080").Enabled() {
		t.Error("empty base domain must be disabled")
	}
	if NewSSOProvider("base.example", "", "", "x").Enabled() {
		t.Error("empty hash must be disabled")
	}
	if !NewSSOProvider("base.example", testHash(t, "pw"), "", "x").Enabled() {
		t.Error("configured provider must be enabled")
	}
}

func TestSSOPassword(t *testing.T) {
	p := NewSSOProvider("base.example", testHash(t, "hunter2"), "secret", "x")
	if !p.CheckPassword("hunter2") {
		t.Error("correct password rejected")
	}
	if p.CheckPassword("wrong") {
		t.Error("wrong password accepted")
	}
}

func TestSSOCookieRoundTripAndTamper(t *testing.T) {
	p := NewSSOProvider("base.example", testHash(t, "pw"), "secret", "x")
	now := time.Unix(1_000_000, 0)
	val := p.signValue(now.Unix()+100, true)

	exp, remember, ok := p.parseValue(val)
	if !ok || !remember || exp != now.Unix()+100 {
		t.Fatalf("roundtrip failed: ok=%v remember=%v exp=%d", ok, remember, exp)
	}
	// Tamper with the signature.
	if _, _, ok := p.parseValue(val + "x"); ok {
		t.Error("tampered signature accepted")
	}
	// A different key must not validate (forgery from another secret).
	other := NewSSOProvider("base.example", testHash(t, "pw"), "other-secret", "x")
	if _, _, ok := other.parseValue(val); ok {
		t.Error("cookie validated under a different signing key")
	}
}

func TestSSOExpiry(t *testing.T) {
	p := NewSSOProvider("base.example", testHash(t, "pw"), "secret", "x")
	past := time.Unix(500, 0)
	val := p.signValue(past.Unix(), false) // already expired
	r := httptest.NewRequest("GET", "/auth/verify", nil)
	r.AddCookie(&http.Cookie{Name: SSOCookieName, Value: val})
	if _, ok := p.verify(r, time.Unix(1000, 0)); ok {
		t.Error("expired cookie accepted")
	}
}

func TestSSOStableKeyAcrossRestart(t *testing.T) {
	hash := testHash(t, "pw")
	// Same hash + empty secret → key derived from hash → identical across "restarts".
	p1 := NewSSOProvider("base.example", hash, "", "x")
	p2 := NewSSOProvider("base.example", hash, "", "x")
	val := p1.signValue(time.Now().Unix()+100, true)
	if _, _, ok := p2.parseValue(val); !ok {
		t.Error("cookie from p1 not valid under p2 — restart would log the user out")
	}
}

func TestSSOCookieAttrs(t *testing.T) {
	p := NewSSOProvider("base.example", testHash(t, "pw"), "secret", "x")
	remember := p.issueFor(true, time.Now())
	if remember.Domain != ".base.example" || !remember.HttpOnly || !remember.Secure {
		t.Errorf("cookie attrs wrong: %+v", remember)
	}
	if remember.SameSite != 2 /* Lax */ {
		t.Errorf("SameSite=%d want Lax(2)", remember.SameSite)
	}
	if remember.MaxAge <= 0 {
		t.Error("remember cookie must be persistent (MaxAge>0)")
	}
	session := p.issueFor(false, time.Now())
	if session.MaxAge != 0 {
		t.Errorf("session cookie must have MaxAge 0 (got %d)", session.MaxAge)
	}
}

func TestSSOSafeRD(t *testing.T) {
	p := NewSSOProvider("base.example", testHash(t, "pw"), "secret", "x")
	def := "https://base.example/"
	cases := map[string]string{
		"https://base.example/x":            "https://base.example/x",
		"https://5173--w--r.base.example/":  "https://5173--w--r.base.example/",
		"https://evil.com/":                 def, // foreign host
		"http://base.example/":              def, // not https
		"https://base.example.evil.com/":    def, // suffix trick
		"":                                  def,
	}
	for in, want := range cases {
		if got := p.safeRD(in); got != want {
			t.Errorf("safeRD(%q)=%q want %q", in, got, want)
		}
	}
}

func TestSSOLoginPageRenders(t *testing.T) {
	p := NewSSOProvider("base.example", testHash(t, "pw"), "secret", "x")
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/auth/login?rd=https://base.example/x", nil)
	p.LoginPage(w, r)
	body := w.Body.String()
	for _, want := range []string{
		`data-testid="auth-login-form"`, `data-testid="auth-password-input"`,
		`data-testid="auth-remember-checkbox"`, `data-testid="auth-submit"`,
		`base.example`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("login page missing %s", want)
		}
	}
}
