// Package main starts the Slotwise web server.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/alexedwards/scs/v2"

	"github.com/ctru0009/slotwise/internal/adapters/email"
	"github.com/ctru0009/slotwise/internal/adapters/postgres"
	"github.com/ctru0009/slotwise/internal/adapters/web"
	"github.com/ctru0009/slotwise/internal/app"
	"github.com/ctru0009/slotwise/internal/clock"
)

// Server settings that are policy rather than deployment: how long a login
// lasts, and how much guessing the login and reset forms tolerate.
const (
	serverAddr         = ":8080"
	defaultBaseURL     = "http://localhost:8080"
	sessionLifetime    = 12 * time.Hour
	sessionIdleTimeout = 2 * time.Hour
	loginLimit         = 10
	loginWindow        = 15 * time.Minute
	resetLimit         = 5
	resetWindow        = time.Hour
	// The reset form hashes the submitted password before it can know whether
	// the token is valid, so its budget is per business and generous enough for
	// somebody retrying a link.
	resetSubmitLimit = 30
	// Booking is unauthenticated, so both public routes are throttled: a create
	// per business and per customer account, and a slot search per business,
	// since one search reads a day of grid for every staff member.
	bookingLimit       = 10
	bookingTenantLimit = 60
	bookingWindow      = 15 * time.Minute
	slotsLimit         = 600
	slotsWindow        = 15 * time.Minute
)

// bootstrapVars are the environment variables that provision the first tenant
// and its owner login; the slug switches the whole group on.
var bootstrapVars = []struct {
	env   string
	value func(*bootstrapEnv) *string
}{
	{"SLOTWISE_BOOTSTRAP_NAME", func(b *bootstrapEnv) *string { return &b.name }},
	{"SLOTWISE_BOOTSTRAP_TIMEZONE", func(b *bootstrapEnv) *string { return &b.timezone }},
	{"SLOTWISE_BOOTSTRAP_OWNER_EMAIL", func(b *bootstrapEnv) *string { return &b.ownerEmail }},
	{"SLOTWISE_BOOTSTRAP_OWNER_PASSWORD", func(b *bootstrapEnv) *string { return &b.ownerPassword }},
}

func main() {
	if err := run(); err != nil {
		slog.Error("exiting", "error", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		return err
	}

	srv, cleanup, err := wire(ctx, cfg, slog.Default())
	if err != nil {
		return err
	}
	defer cleanup()

	serveErr := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- fmt.Errorf("serving http: %w", err)
			return
		}
		serveErr <- nil
	}()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}

	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutting down: %w", err)
	}
	return nil
}

// bootstrapEnv is the optional startup provisioning group. A half-filled group
// is a configuration error rather than a silently skipped tenant.
type bootstrapEnv struct {
	slug          string
	name          string
	timezone      string
	ownerEmail    string
	ownerPassword string
}

// set reports whether any variable of the group was provided.
func (b bootstrapEnv) set() bool {
	return b.slug != "" || b.name != "" || b.timezone != "" || b.ownerEmail != "" || b.ownerPassword != ""
}

// config is the deployment's environment plus the two seams tests replace: the
// clock rate limiting reads and the sender the reset link goes to. A nil clock
// means the system clock; a nil sender means the logging one.
type config struct {
	databaseURL  string
	baseURL      string
	cookieSecure bool
	bootstrap    bootstrapEnv
	// cancelSecret signs the public cancel links. It is required: without it
	// the routes would either refuse every link or accept forged ones.
	cancelSecret string
	clock        clock.Clock
	sender       app.Sender
}

// loadConfig reads the environment. getenv is a parameter so tests can supply a
// map instead of the process environment.
func loadConfig(getenv func(string) string) (config, error) {
	cfg := config{
		databaseURL:  getenv("DATABASE_URL"),
		baseURL:      getenv("SLOTWISE_BASE_URL"),
		cookieSecure: getenv("SLOTWISE_COOKIE_SECURE") == "1",
		bootstrap:    bootstrapEnv{slug: getenv("SLOTWISE_BOOTSTRAP_SLUG")},
		cancelSecret: getenv("SLOTWISE_CANCEL_SECRET"),
	}
	if cfg.databaseURL == "" {
		return config{}, errors.New("DATABASE_URL is required")
	}
	if cfg.baseURL == "" {
		cfg.baseURL = defaultBaseURL
	}
	if cfg.cancelSecret == "" {
		return config{}, errors.New("SLOTWISE_CANCEL_SECRET is required")
	}
	if err := cfg.bootstrap.load(getenv); err != nil {
		return config{}, err
	}
	return cfg, nil
}

// load fills the rest of the provisioning group, refusing any partial
// configuration: a deploy that meant to create a tenant must not quietly serve
// without one. The whole group is read before it is judged, because a group
// that only looks empty is exactly the mistake this has to catch.
func (b *bootstrapEnv) load(getenv func(string) string) error {
	for _, v := range bootstrapVars {
		*v.value(b) = getenv(v.env)
	}
	if !b.set() {
		return nil
	}
	if b.slug == "" {
		return errors.New("SLOTWISE_BOOTSTRAP_SLUG is required when other SLOTWISE_BOOTSTRAP_* variables are set")
	}
	for _, v := range bootstrapVars {
		if *v.value(b) == "" {
			return fmt.Errorf("%s is required when SLOTWISE_BOOTSTRAP_SLUG is set", v.env)
		}
	}
	return nil
}

// wire builds the server: database, session store, use cases and the HTTP
// layer. It returns the server and the cleanup that closes the database, so
// main and the end-to-end test drive exactly the same composition. A failure
// closes the pool before returning: the caller has no cleanup to run yet, and a
// half-started server must not hold connections open.
func wire(ctx context.Context, cfg config, logger *slog.Logger) (*http.Server, func(), error) {
	db, err := postgres.New(ctx, cfg.databaseURL)
	if err != nil {
		return nil, nil, fmt.Errorf("opening the database: %w", err)
	}
	cleanup := db.Close
	started := false
	defer func() {
		if !started {
			cleanup()
		}
	}()

	clk, sender := cfg.clock, cfg.sender
	if clk == nil {
		clk = clock.System{}
	}
	if sender == nil {
		sender = email.NewLogSender(logger)
	}

	auth := app.NewAuth(db, db, db, sender, clk)

	if err := bootstrap(ctx, auth, cfg, logger); err != nil {
		return nil, nil, err
	}

	// Expired sessions are unreachable through session_find, but they would
	// otherwise accumulate forever.
	if err := db.PurgeSessions(ctx); err != nil {
		return nil, nil, fmt.Errorf("purging expired sessions: %w", err)
	}

	views, err := web.LoadViews()
	if err != nil {
		return nil, nil, fmt.Errorf("loading views: %w", err)
	}
	signer, err := app.NewCancelSigner(cfg.cancelSecret)
	if err != nil {
		return nil, nil, fmt.Errorf("building the cancel signer: %w", err)
	}

	sessions := scs.New()
	sessions.Lifetime = sessionLifetime
	sessions.IdleTimeout = sessionIdleTimeout
	sessions.Cookie.Name = "slotwise_session"
	sessions.Cookie.Secure = cfg.cookieSecure
	sessions.Store = postgres.NewSessionStore(ctx, db)

	handler := web.New(web.Deps{
		Sessions:      sessions,
		Auth:          auth,
		Services:      app.NewServices(db),
		Staff:         app.NewStaff(db),
		Availability:  app.NewAvailability(db, db, clk),
		Bookings:      app.NewBookings(db, db, db, clk, signer),
		Views:         views,
		Clock:         clk,
		Login:         web.NewLimiter(loginLimit, loginWindow),
		Reset:         web.NewLimiter(resetLimit, resetWindow),
		ResetSubmit:   web.NewLimiter(resetSubmitLimit, resetWindow),
		BookingWrites: web.NewLimiter(bookingTenantLimit, bookingWindow),
		BookingPosts:  web.NewLimiter(bookingLimit, bookingWindow),
		SlotSearches:  web.NewLimiter(slotsLimit, slotsWindow),
		BaseURL:       cfg.baseURL,
	})

	// The probe endpoints sit outside the application router so they stay
	// independent of it.
	root := http.NewServeMux()
	root.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	root.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	root.Handle("/", handler)

	srv := &http.Server{
		Addr:              serverAddr,
		Handler:           root,
		ReadHeaderTimeout: 5 * time.Second,
	}
	started = true
	return srv, cleanup, nil
}

// bootstrap provisions the environment's tenant and owner when the group is
// configured. It is idempotent: a restart reports owner_created=false.
func bootstrap(ctx context.Context, auth *app.Auth, cfg config, logger *slog.Logger) error {
	if !cfg.bootstrap.set() {
		return nil
	}
	created, err := auth.Bootstrap(ctx, app.BootstrapInput{
		Slug:          cfg.bootstrap.slug,
		Name:          cfg.bootstrap.name,
		Timezone:      cfg.bootstrap.timezone,
		OwnerEmail:    cfg.bootstrap.ownerEmail,
		OwnerPassword: cfg.bootstrap.ownerPassword,
	})
	if err != nil {
		return fmt.Errorf("bootstrapping tenant %s: %w", cfg.bootstrap.slug, err)
	}
	logger.Info("bootstrap", "tenant", cfg.bootstrap.slug, "owner_created", created)
	return nil
}
