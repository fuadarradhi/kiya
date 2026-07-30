package kiya_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fuadarradhi/kiya"
)

func TestBrowserCookie(t *testing.T) {
	app, err := kiya.New(
		kiya.WithSession("super-secret-key-1234567890123456"),
		kiya.WithBrowserCookie("browser"),
	)
	if err != nil {
		t.Fatalf("failed to create app: %v", err)
	}

	var lastBrowserID string
	var lastValidBrowser bool

	app.Get("/check", func(c *kiya.Context) error {
		lastBrowserID = c.BrowserID()
		lastValidBrowser = c.ValidBrowser()
		return c.String(http.StatusOK, "ok")
	})

	userAgent1 := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/120.0.0.0"
	userAgent2 := "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Safari/605.1.15"

	var issuedCookie *http.Cookie

	// Test 1: First request (no cookie yet) -> ValidBrowser should be false, new cookie issued
	t.Run("First Visit - New Browser Cookie Issued", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/check", nil)
		req.Header.Set("User-Agent", userAgent1)
		rec := httptest.NewRecorder()

		app.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		if lastValidBrowser != false {
			t.Errorf("expected ValidBrowser=false on first visit, got %v", lastValidBrowser)
		}
		if lastBrowserID == "" {
			t.Errorf("expected BrowserID to be set, got empty")
		}

		// Find response cookie "browser"
		cookies := rec.Result().Cookies()
		for _, ck := range cookies {
			if ck.Name == "browser" {
				issuedCookie = ck
				if ck.MaxAge != 30*86400 {
					t.Errorf("expected MaxAge 2592000 (30 days), got %d", ck.MaxAge)
				}
				if ck.Path != "/" {
					t.Errorf("expected Path '/', got '%s'", ck.Path)
				}
			}
		}
		if issuedCookie == nil {
			t.Fatalf("cookie 'browser' not found in response cookies")
		}
	})

	// Test 2: Second request with valid cookie & same User-Agent -> ValidBrowser should be true
	t.Run("Subsequent Visit - Valid Browser Cookie", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/check", nil)
		req.Header.Set("User-Agent", userAgent1)
		req.AddCookie(issuedCookie)
		rec := httptest.NewRecorder()

		app.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		if lastValidBrowser != true {
			t.Errorf("expected ValidBrowser=true on valid visit, got %v", lastValidBrowser)
		}
		if lastBrowserID != issuedCookie.Value {
			t.Errorf("expected BrowserID '%s', got '%s'", issuedCookie.Value, lastBrowserID)
		}
	})

	// Test 3: Request with stolen/copied cookie on different User-Agent -> ValidBrowser should be false
	t.Run("Stolen Cookie on Different User-Agent - Rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/check", nil)
		req.Header.Set("User-Agent", userAgent2) // Different UA!
		req.AddCookie(issuedCookie)
		rec := httptest.NewRecorder()

		app.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		if lastValidBrowser != false {
			t.Errorf("expected ValidBrowser=false when User-Agent changes, got %v", lastValidBrowser)
		}
		if lastBrowserID == issuedCookie.Value {
			t.Errorf("expected new BrowserID to be generated, got same stolen ID")
		}
	})
}
