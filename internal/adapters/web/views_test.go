package web

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/ctru0009/slotwise/internal/domain"
)

// TestLoadViewsRendersEveryPage proves every embedded page parses and renders
// a complete document with its data.
func TestLoadViewsRendersEveryPage(t *testing.T) {
	t.Parallel()
	views, err := LoadViews()
	if err != nil {
		t.Fatalf("LoadViews() error = %v", err)
	}

	tenant := domain.Tenant{ID: uuid.New(), Slug: "demo", Name: "Demo Studio", Timezone: "Europe/Berlin"}
	serviceID, staffID := uuid.New(), uuid.New()

	pages := []struct {
		name string
		data any
		want []string
	}{
		{
			name: PageStart,
			data: StartPage{Slug: "demo"},
			want: []string{`action="/login"`, `name="slug"`, `value="demo"`},
		},
		{
			name: PageLogin,
			data: LoginPage{Tenant: tenant},
			want: []string{"Sign in to Demo Studio", `action="/app/demo/login"`, `type="email"`, `type="password"`, `/app/demo/forgot`},
		},
		{
			name: PageForgot,
			data: ForgotPage{Tenant: tenant},
			want: []string{`action="/app/demo/forgot"`, "Send reset link", `/app/demo/login`},
		},
		{
			name: PageForgot,
			data: ForgotPage{Tenant: tenant, Sent: true},
			want: []string{"If that email is registered, a reset link is on its way."},
		},
		{
			name: PageReset,
			data: ResetPage{Tenant: tenant, Token: "tok-123"},
			want: []string{`action="/app/demo/reset"`, `value="tok-123"`, `minlength="12"`},
		},
		{
			name: PageDashboard,
			data: DashboardPage{
				Tenant: tenant,
				User:   domain.User{Email: "owner@example.com", Role: domain.RoleOwner},
				Services: []domain.Service{{
					ID: serviceID, Name: "Haircut", DurationMinutes: 30,
					BufferMinutes: 5, PriceCents: 4250, Active: true,
				}},
				Staff: []domain.Staff{{ID: staffID, Name: "Ada", Email: "ada@example.com"}},
			},
			want: []string{
				"Demo Studio", "owner@example.com", "(owner)", `action="/app/logout"`,
				`action="/app/services"`, "Haircut", "42.50", "Active",
				`action="/app/services/` + serviceID.String() + `"`,
				`action="/app/services/` + serviceID.String() + `/active"`, "Deactivate",
				`action="/app/staff"`, "ada@example.com",
				`action="/app/staff/` + staffID.String() + `"`,
				`action="/app/staff/` + staffID.String() + `/active"`, "Activate",
			},
		},
		{
			name: PageDashboard,
			data: DashboardPage{Tenant: tenant, User: domain.User{Email: "owner@example.com"}},
			want: []string{"No services yet.", "No staff yet."},
		},
		{
			name: PageError,
			data: ErrorPage{Status: http.StatusNotFound, Title: http.StatusText(http.StatusNotFound), Message: "no such tenant"},
			want: []string{"404 Not Found", "no such tenant", "/login"},
		},
	}

	for _, page := range pages {
		var buf bytes.Buffer
		if err := views.Render(&buf, page.name, page.data); err != nil {
			t.Errorf("Render(%s) error = %v", page.name, err)
			continue
		}
		body := buf.String()
		if !strings.HasPrefix(body, "<!doctype html>") {
			t.Errorf("Render(%s) did not emit an HTML5 document", page.name)
		}
		if !strings.Contains(body, "</html>") {
			t.Errorf("Render(%s) did not close the document", page.name)
		}
		for _, want := range page.want {
			if !strings.Contains(body, want) {
				t.Errorf("Render(%s) body does not contain %q", page.name, want)
			}
		}
	}
}

func TestRenderUnknownPage(t *testing.T) {
	t.Parallel()
	views, err := LoadViews()
	if err != nil {
		t.Fatalf("LoadViews() error = %v", err)
	}
	var buf bytes.Buffer
	if err := views.Render(&buf, "nope", nil); err == nil {
		t.Fatal("Render(unknown page) error = nil, want an error")
	}
	if buf.Len() != 0 {
		t.Errorf("Render(unknown page) wrote %d bytes, want none", buf.Len())
	}
}

func TestMoney(t *testing.T) {
	t.Parallel()
	cases := []struct {
		cents int
		want  string
	}{
		{cents: 0, want: "0.00"},
		{cents: 5, want: "0.05"},
		{cents: 99, want: "0.99"},
		{cents: 100, want: "1.00"},
		{cents: 4250, want: "42.50"},
		{cents: -5, want: "-0.05"},
		{cents: -100, want: "-1.00"},
		{cents: -4250, want: "-42.50"},
	}
	for _, tc := range cases {
		if got := money(tc.cents); got != tc.want {
			t.Errorf("money(%d) = %q, want %q", tc.cents, got, tc.want)
		}
	}
}

func TestParseForm(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/app/demo/login", strings.NewReader("email=owner%40example.com&password=hunter2hunter2"))
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

func TestFailRendersErrorPage(t *testing.T) {
	t.Parallel()
	views, err := LoadViews()
	if err != nil {
		t.Fatalf("LoadViews() error = %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/app/nope/login", nil)
	views.fail(rec, req, http.StatusNotFound, "no such tenant")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want %q", got, "no-store")
	}
	for _, want := range []string{"404 Not Found", "no such tenant"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("body does not contain %q", want)
		}
	}
}
