package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuth_OpenMode_AlwaysAllows(t *testing.T) {
	a, err := New("")
	if err != nil {
		t.Fatal(err)
	}
	if !a.Open() {
		t.Fatal("expected open mode")
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/repos", nil)
	called := false
	a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })).ServeHTTP(rec, req)
	if !called {
		t.Error("middleware blocked open-mode request")
	}
	// Should have set a cookie on the response.
	cookies := rec.Result().Cookies()
	found := false
	for _, c := range cookies {
		if c.Name == CookieName {
			found = true
		}
	}
	if !found {
		t.Error("open-mode middleware should set cookie")
	}
}

func TestAuth_TokenMode_RequiresCookieOrBearer(t *testing.T) {
	a, err := New("secret")
	if err != nil {
		t.Fatal(err)
	}

	// No credentials → 401.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/repos", nil)
	a.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("handler should not run without auth")
	})).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}

	// Bearer ok.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/repos", nil)
	req.Header.Set("Authorization", "Bearer secret")
	called := false
	a.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })).ServeHTTP(rec, req)
	if !called {
		t.Error("Bearer-authed request should pass")
	}

	// Cookie ok.
	cookie := a.Cookie()
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/repos", nil)
	req.AddCookie(cookie)
	called = false
	a.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })).ServeHTTP(rec, req)
	if !called {
		t.Error("cookie-authed request should pass")
	}

	// Wrong cookie → 401.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/repos", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: "garbage"})
	a.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("handler should not run with bad cookie")
	})).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for bad cookie, got %d", rec.Code)
	}
}

func TestAuth_AuthHandler_SetsCookieAndRedirects(t *testing.T) {
	a, _ := New("secret")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth?token=secret", nil)
	a.AuthHandler(rec, req)
	if rec.Code != http.StatusFound {
		t.Errorf("expected 302, got %d", rec.Code)
	}
	if rec.Header().Get("Location") != "/" {
		t.Errorf("expected redirect to /, got %q", rec.Header().Get("Location"))
	}
	cookies := rec.Result().Cookies()
	found := false
	for _, c := range cookies {
		if c.Name == CookieName {
			found = true
		}
	}
	if !found {
		t.Error("auth handler should set cookie")
	}
}

func TestAuth_AuthHandler_InvalidToken(t *testing.T) {
	a, _ := New("secret")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth?token=wrong", nil)
	a.AuthHandler(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}
