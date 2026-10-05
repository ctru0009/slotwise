package web

import (
	"net/http"

	"github.com/alexedwards/scs/v2"

	"github.com/ctru0009/slotwise/internal/app"
	"github.com/ctru0009/slotwise/internal/clock"
)

// Deps is everything the HTTP layer needs from the outside.
type Deps struct {
	Sessions *scs.SessionManager
	Auth     *app.Auth
	Services *app.Services
	Staff    *app.Staff
	Views    *Views
	Clock    clock.Clock
	Login    *Limiter
	Reset    *Limiter
	BaseURL  string
}

// server holds the dependencies every handler shares.
type server struct {
	deps Deps
}

// New builds the web handler: the route table behind session loading, a
// no-store cache policy and cross-origin protection. Cross-origin protection
// is outermost, so a rejected cross-origin POST never reaches the session.
func New(deps Deps) http.Handler {
	s := &server{deps: deps}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.startRedirect)
	mux.HandleFunc("GET /login", s.loginStart)
	mux.HandleFunc("POST /app/logout", s.endSession)
	mux.HandleFunc("GET /app", s.withUser(s.dashboard))

	// The tenant account routes and the services/staff routes cannot share
	// one mux: POST /app/{slug}/login and POST /app/services/{id} overlap at
	// /app/services/login without either being more specific, a pair
	// http.ServeMux refuses to register. Giving the literal services/staff
	// prefixes their own mux keeps the intended precedence, so a path under
	// them is never read as a tenant slug.
	servicesMux := http.NewServeMux()
	servicesMux.HandleFunc("POST /app/services", s.withUser(s.createService))
	servicesMux.HandleFunc("POST /app/services/{id}", s.withUser(s.updateService))
	servicesMux.HandleFunc("POST /app/services/{id}/active", s.withUser(s.setServiceActive))
	mux.Handle("/app/services", servicesMux)
	mux.Handle("/app/services/", servicesMux)

	staffMux := http.NewServeMux()
	staffMux.HandleFunc("POST /app/staff", s.withUser(s.createStaff))
	staffMux.HandleFunc("POST /app/staff/{id}", s.withUser(s.updateStaff))
	staffMux.HandleFunc("POST /app/staff/{id}/active", s.withUser(s.setStaffActive))
	mux.Handle("/app/staff", staffMux)
	mux.Handle("/app/staff/", staffMux)

	tenantMux := http.NewServeMux()
	tenantMux.HandleFunc("GET /app/{slug}/login", s.loginForm)
	tenantMux.HandleFunc("POST /app/{slug}/login", s.loginSubmit)
	tenantMux.HandleFunc("GET /app/{slug}/forgot", s.forgotForm)
	tenantMux.HandleFunc("POST /app/{slug}/forgot", s.forgotSubmit)
	tenantMux.HandleFunc("GET /app/{slug}/reset", s.resetForm)
	tenantMux.HandleFunc("POST /app/{slug}/reset", s.resetSubmit)
	mux.Handle("/", tenantMux)

	return http.NewCrossOriginProtection().Handler(noStore(deps.Sessions.LoadAndSave(mux)))
}
