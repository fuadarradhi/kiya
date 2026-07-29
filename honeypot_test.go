package kiya_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/fuadarradhi/kiya"
)

func TestHoneypotProtection(t *testing.T) {
	app, err := kiya.New(
		kiya.WithHoneypot("honeypot_token"),
		kiya.WithoutHoneypot("/api"),
	)
	if err != nil {
		t.Fatalf("failed to create app: %v", err)
	}

	app.Post("/submit", func(c *kiya.Context) error {
		return c.String(http.StatusOK, "success")
	})
	app.Post("/api/data", func(c *kiya.Context) error {
		return c.String(http.StatusOK, "api success")
	})

	// Test 1: Clean request (human) - honeypot empty
	t.Run("Clean Submission", func(t *testing.T) {
		form := url.Values{}
		form.Set("name", "John")
		form.Set("honeypot_token", "") // empty field

		req := httptest.NewRequest(http.MethodPost, "/submit", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()

		req.Header.Set("X-Request-Id", "test-1")
		app.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("expected status 200, got %d", rec.Code)
		}
	})

	// Test 2: Spam bot submission - honeypot filled
	t.Run("Bot Spam Submission", func(t *testing.T) {
		form := url.Values{}
		form.Set("name", "Spam Bot")
		form.Set("honeypot_token", "http://spam-link.com") // bot filled it

		req := httptest.NewRequest(http.MethodPost, "/submit", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()

		app.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected status 400 Bad Request, got %d", rec.Code)
		}
	})

	// Test 3: Exempt path (/api)
	t.Run("Exempt API Path", func(t *testing.T) {
		form := url.Values{}
		form.Set("honeypot_token", "bot-data")

		req := httptest.NewRequest(http.MethodPost, "/api/data", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()

		app.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("expected status 200 for exempt path, got %d", rec.Code)
		}
	})
}
