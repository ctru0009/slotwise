package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestParseForm pins what every form handler relies on: the submitted values are
// readable, and the response that depends on them is uncacheable.
func TestParseForm(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/app/demo/login",
		strings.NewReader("email=owner%40example.com&password=hunter2hunter2"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := parseForm(rec, req); err != nil {
		t.Fatalf("parseForm() error = %v", err)
	}
	if got := req.PostForm.Get("email"); got != "owner@example.com" {
		t.Errorf("PostForm email = %q, want %q", got, "owner@example.com")
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want %q", got, "no-store")
	}
}

// TestParseFormRejectsOversizedBody pins the cap: a body past maxFormBytes is an
// error rather than something the server buffers.
func TestParseFormRejectsOversizedBody(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	body := "email=" + strings.Repeat("a", maxFormBytes) + "%40example.com"
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/app/demo/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := parseForm(rec, req); err == nil {
		t.Fatal("parseForm() with an oversized body error = nil, want an error")
	}
}
