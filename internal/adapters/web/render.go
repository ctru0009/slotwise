package web

import (
	"fmt"
	"log/slog"
	"net/http"
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

// fail renders the error page with the given HTTP status. Rendering failures
// are logged rather than surfaced, since the response has already started.
func (v *Views) fail(w http.ResponseWriter, r *http.Request, status int, message string) {
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	page := ErrorPage{Status: status, Title: http.StatusText(status), Message: message}
	if err := v.Render(w, PageError, page); err != nil {
		slog.ErrorContext(r.Context(), "rendering error page", "path", r.URL.Path, "status", status, "err", err)
	}
}
