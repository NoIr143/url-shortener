// Package webui implements UI-006, the mandatory public creation page,
// per docs/UI_UX_DESIGN.md Section 3 and ADR-018 (server-rendered Go
// templates, no-JavaScript functional baseline, minimal progressive
// enhancement for clipboard/duplicate-submit only). This is T4-01's
// low-fidelity prototype: functionally complete against the documented
// nine states, not visually polished.
package webui

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"html/template"
	"net/http"

	"url-shortener/internal/api"
)

// Store mirrors api.MappingStore's Create signature so the same backend
// (real or fake) can satisfy both the JSON API and this UI handler,
// consistent with IR-UI-001's "same creation rules and outcomes."

//go:embed templates/page.html
var templateFS embed.FS

var tmpl = template.Must(template.ParseFS(templateFS, "templates/page.html"))

// pageData drives the single page template; State selects which of the
// documented states (docs/UI_UX_DESIGN.md Section 3.3) renders.
type pageData struct {
	State       string
	Destination string
	Errors      []string
	ShortURL    string
	RetryAfter  string
	Nonce       string
}

type Store interface {
	Create(ctx context.Context, shortKey, destination string) (created bool, resolvedKey string, err error)
}

// Handler serves GET / (state 1: Initial) and POST / (states 3-8) per
// docs/UI_UX_DESIGN.md Section 3.3. It calls the same validation used by
// the JSON API (internal/api.ValidateDestination), satisfying IR-UI-001's
// requirement that the UI expose the same creation rules as the API.
type Handler struct {
	store   Store
	keyGen  func() string
	limiter *api.IPRateLimiter
}

func New(store Store, keyGen func() string, limiter *api.IPRateLimiter) *Handler {
	return &Handler{store: store, keyGen: keyGen, limiter: limiter}
}

func nonce() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.StdEncoding.EncodeToString(b)
}

func (h *Handler) render(w http.ResponseWriter, status int, data pageData) {
	n := nonce()
	data.Nonce = n
	w.Header().Set("Content-Security-Policy",
		"default-src 'self'; script-src 'nonce-"+n+"'; style-src 'unsafe-inline'; form-action 'self'")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	// html/template auto-escapes every field above; the destination the
	// visitor typed is echoed back into the form value (for correction
	// per FR-026) through the template's contextual auto-escaping, never
	// via string concatenation, so it cannot break out of the attribute.
	_ = tmpl.Execute(w, data)
}

// ServeCreatePage handles GET / — state 1, Initial.
func (h *Handler) ServeCreatePage(w http.ResponseWriter, r *http.Request) {
	h.render(w, http.StatusOK, pageData{State: "initial"})
}

// HandleSubmit handles POST / — states 3 through 8, per Section 3.3.
func (h *Handler) HandleSubmit(w http.ResponseWriter, r *http.Request) {
	clientIP := r.RemoteAddr
	if !h.limiter.Allow(clientIP) {
		// State 7: Throttled (FR-014/FR-015).
		h.render(w, http.StatusTooManyRequests, pageData{
			State:       "throttled",
			Destination: r.FormValue("destination"),
			RetryAfter:  "a minute",
		})
		return
	}

	dest := r.FormValue("destination")
	if err := api.ValidateDestination(dest); err != nil {
		// State 3: Validation error (FR-002/003/026). The submitted value
		// is retained, never discarded.
		h.render(w, http.StatusBadRequest, pageData{
			State:       "validation_error",
			Destination: dest,
			Errors:      []string{humanizeValidationError(err)},
		})
		return
	}

	key := h.keyGen()
	created, resolvedKey, err := h.store.Create(r.Context(), key, dest)
	if err != nil {
		// State 8: Temporary/ambiguous failure (FR-016/FR-028/FR-008) — no
		// key is ever shown for a failed/uncertain attempt.
		h.render(w, http.StatusServiceUnavailable, pageData{
			State:       "temporary_failure",
			Destination: dest,
		})
		return
	}

	state := "success_new" // state 4
	if !created {
		state = "success_existing" // state 5 (BR-005 exact repeat)
	}
	h.render(w, http.StatusOK, pageData{
		State:    state,
		ShortURL: "https://short.example/" + resolvedKey,
	})
}

// humanizeValidationError maps internal validation errors to the plain,
// action-oriented copy docs/UI_UX_DESIGN.md requires — never the
// destination itself, and never a raw Go error string.
func humanizeValidationError(err error) string {
	switch err {
	case api.ErrDestinationEmpty:
		return "Enter a destination URL."
	case api.ErrDestinationTooLong:
		return "That address is too long. The maximum is 2048 characters."
	case api.ErrUnsupportedScheme:
		return "The address must start with http:// or https://."
	case api.ErrRelativeDestination:
		return "Enter a complete address, including http:// or https://."
	case api.ErrUserInfoPresent:
		return "The address must not include a username or password."
	case api.ErrControlCharacters:
		return "That address contains a character that isn't allowed."
	default:
		return "That address isn't valid. Check for a missing or extra character."
	}
}
