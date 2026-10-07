package web

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/a-h/templ"

	"github.com/ctru0009/slotwise/internal/adapters/web/views"
)

// maxFormBytes caps the size of a parsed request body.
const maxFormBytes = 1 << 16

// parseForm reads the request's form values, refusing bodies larger than
// 64 KiB. Responses that depend on form input must not be cached.
func parseForm(w http.ResponseWriter, r *http.Request) error {
	w.Header().Set("Cache-Control", "no-store")
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		return fmt.Errorf("parsing form: %w", err)
	}
	return nil
}

// renderPage writes one component to the response. A render failure can only be
// logged: the response has already started by then.
func renderPage(ctx context.Context, w http.ResponseWriter, component templ.Component) {
	if err := component.Render(ctx, w); err != nil {
		slog.ErrorContext(ctx, "rendering page", "err", err)
	}
}

// fail renders the error page with the given HTTP status.
func fail(w http.ResponseWriter, r *http.Request, status int, message string) {
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	renderPage(r.Context(), w, views.Error(views.ErrorPage{
		Status:  status,
		Title:   http.StatusText(status),
		Message: message,
	}))
}
