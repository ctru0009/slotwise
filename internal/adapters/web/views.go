package web

import (
	"embed"
	"fmt"
	"html/template"
	"io"

	"github.com/ctru0009/slotwise/internal/domain"
)

// The pages the web package can render. Each name matches one
// templates/<name>.html file, which defines a "content" template the shared
// layout fills in.
const (
	PageStart     = "start"
	PageLogin     = "login"
	PageForgot    = "forgot"
	PageReset     = "reset"
	PageDashboard = "dashboard"
	PageError     = "error"
)

// pageNames lists every recognised page. LoadViews parses each of them.
var pageNames = []string{
	PageStart,
	PageLogin,
	PageForgot,
	PageReset,
	PageDashboard,
	PageError,
}

//go:embed templates/*.html
var templateFS embed.FS

// StartPage is the tenant slug form served at GET /login.
type StartPage struct {
	Slug  string
	Error string
}

// LoginPage is the sign-in form for one tenant.
type LoginPage struct {
	Tenant domain.Tenant
	Notice string
	Error  string
}

// ForgotPage is the password reset request form for one tenant.
type ForgotPage struct {
	Tenant domain.Tenant
	Sent   bool
}

// ResetPage is the new-password form reached from a reset link.
type ResetPage struct {
	Tenant domain.Tenant
	Token  string
	Error  string
}

// DashboardPage is the authenticated overview of a tenant's services and
// staff, with the forms that change them. A write either redirects here or
// re-renders with an Error, so there is no notice to show.
type DashboardPage struct {
	Tenant   domain.Tenant
	User     domain.User
	Services []domain.Service
	Staff    []domain.Staff
	Error    string
}

// ErrorPage renders an HTTP status and a human-readable message.
type ErrorPage struct {
	Status  int
	Title   string
	Message string
}

// Views holds the parsed templates, one full set per page.
type Views struct {
	pages map[string]*template.Template
}

// LoadViews parses the embedded templates into a renderable Views.
func LoadViews() (*Views, error) {
	views := &Views{pages: make(map[string]*template.Template, len(pageNames))}
	for _, page := range pageNames {
		// Every page is its own template set because the shared layout
		// calls "content", which each page file defines.
		tmpl, err := template.New("layout.html").
			Funcs(templateFuncs()).
			ParseFS(templateFS, "templates/layout.html", "templates/"+page+".html")
		if err != nil {
			return nil, fmt.Errorf("parsing %s template: %w", page, err)
		}
		views.pages[page] = tmpl
	}
	return views, nil
}

// Render writes the named page to w.
func (v *Views) Render(w io.Writer, page string, data any) error {
	tmpl, ok := v.pages[page]
	if !ok {
		return fmt.Errorf("rendering unknown page %q", page)
	}
	if err := tmpl.ExecuteTemplate(w, "layout.html", data); err != nil {
		return fmt.Errorf("rendering %s page: %w", page, err)
	}
	return nil
}

// templateFuncs returns the helpers every page template can call.
func templateFuncs() template.FuncMap {
	return template.FuncMap{"money": money}
}

// money renders an integer number of cents as a decimal amount, for example
// 1234 as "12.34" and -5 as "-0.05". It stays in integer arithmetic so no
// rounding can creep in.
func money(cents int) string {
	whole, frac := cents/100, cents%100
	if frac < 0 {
		frac = -frac
	}
	if whole == 0 && cents < 0 {
		return fmt.Sprintf("-0.%02d", frac)
	}
	return fmt.Sprintf("%d.%02d", whole, frac)
}
