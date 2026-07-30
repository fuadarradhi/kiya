package kiya_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fuadarradhi/kiya"
)

func TestSessionResolver(t *testing.T) {
	app, err := kiya.New(
		kiya.WithSession("super-secret-key-1234567890123456"),
		kiya.WithSessionResolver(func(r *http.Request) (string, string) {
			if strings.HasPrefix(r.URL.Path, "/manage") {
				return "manage_session", "/manage"
			}
			return "site_session", "/"
		}),
	)
	if err != nil {
		t.Fatalf("failed to create app: %v", err)
	}

	app.Get("/site-route", func(c *kiya.Context) error {
		c.Session().Set("user", "public_user")
		return c.String(http.StatusOK, "site_ok")
	})

	app.Get("/manage/dashboard", func(c *kiya.Context) error {
		c.Session().Set("user", "admin_user")
		return c.String(http.StatusOK, "manage_ok")
	})

	// Test 1: Public route sets site_session cookie with Path=/
	t.Run("Public Route Session", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/site-route", nil)
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("expected status 200, got %d", rec.Code)
		}

		cookies := rec.Result().Cookies()
		found := false
		for _, ck := range cookies {
			if ck.Name == "site_session" {
				found = true
				if ck.Path != "/" {
					t.Errorf("expected cookie Path '/' for site_session, got '%s'", ck.Path)
				}
			}
		}
		if !found {
			t.Errorf("cookie 'site_session' not found in response cookies: %v", cookies)
		}
	})

	// Test 2: Admin route sets manage_session cookie with Path=/manage
	t.Run("Admin Route Session", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/manage/dashboard", nil)
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("expected status 200, got %d", rec.Code)
		}

		cookies := rec.Result().Cookies()
		found := false
		for _, ck := range cookies {
			if ck.Name == "manage_session" {
				found = true
				if ck.Path != "/manage" {
					t.Errorf("expected cookie Path '/manage' for manage_session, got '%s'", ck.Path)
				}
			}
		}
		if !found {
			t.Errorf("cookie 'manage_session' not found in response cookies: %v", cookies)
		}
	})
}
