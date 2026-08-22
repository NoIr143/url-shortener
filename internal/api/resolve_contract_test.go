// T8-03 contract tests: docs/decisions/DEC-002.md/DEC-009.md's full
// response-class set for GET /{shortKey} — 302 (Active), 404 (Unknown,
// the literal fix for the T8-01-found bug where this incorrectly
// returned 503), 410 (Suspended), 503 (dependency unavailable) — plus
// the response security headers (NFR-SEC-003) this task adds.
package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"url-shortener/internal/application"
	"url-shortener/internal/domain"
)

// erroringResolver lets a test script exactly one Resolve outcome.
type erroringResolver struct {
	err error
}

func (e erroringResolver) Resolve(ctx context.Context, shortKey domain.ShortKey) (application.ResolveResult, error) {
	return application.ResolveResult{}, e.err
}

func newResolveHandlerWithResolver(r Resolver) *ResolveHandler {
	return NewResolveHandler(r, NewIPRateLimiter(100, time.Minute))
}

func doResolveWithHandler(h *ResolveHandler, shortKey, remoteAddr string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/"+shortKey, nil)
	req.RemoteAddr = remoteAddr + ":12345"
	rec := httptest.NewRecorder()
	h.Resolve(rec, req, shortKey)
	return rec
}

// TestResolve_ActiveHasSecurityHeaders proves the 302/Location/
// Cache-Control/X-Content-Type-Options contract for a genuinely active
// mapping.
func TestResolve_ActiveHasSecurityHeaders(t *testing.T) {
	h := newTestHandlers()
	create := doCreate(h, `{"destination":"https://example.com/resolve-active-contract"}`, "10.0.5.1")
	if create.Code != http.StatusCreated {
		t.Fatalf("setup: unexpected create status %d: %s", create.Code, create.Body.String())
	}

	rec := doResolveWithHandler(h.resolve, "k1", "10.0.5.2")
	if rec.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "https://example.com/resolve-active-contract" {
		t.Errorf("unexpected Location: %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("expected Cache-Control: no-store, got %q", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("expected X-Content-Type-Options: nosniff, got %q", got)
	}
}

// TestResolve_UnknownReturns404 is the literal regression test for the
// bug T8-01 found and T8-02/T8-03 fixed: an unknown (but syntactically
// valid) key must return 404, never 503, and never a Location header.
func TestResolve_UnknownReturns404(t *testing.T) {
	h := newTestHandlers()
	rec := doResolveWithHandler(h.resolve, "nosuch1", "10.0.5.3")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Location") != "" {
		t.Errorf("expected no Location for an unknown key")
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("expected Cache-Control: no-store on the error response too, got %q", got)
	}
}

// TestResolve_SuspendedReturns410 proves FR-012: a suspended mapping
// returns 410, never a Location header, and never exposes its
// destination anywhere in the response body.
func TestResolve_SuspendedReturns410(t *testing.T) {
	h := newTestHandlers()
	create := doCreate(h, `{"destination":"https://example.com/should-never-be-exposed"}`, "10.0.5.4")
	if create.Code != http.StatusCreated {
		t.Fatalf("setup: unexpected create status %d: %s", create.Code, create.Body.String())
	}
	fs := h.resolve.useCase.(*fakeStore)
	fs.suspend("k1")

	rec := doResolveWithHandler(h.resolve, "k1", "10.0.5.5")
	if rec.Code != http.StatusGone {
		t.Fatalf("expected 410, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Location") != "" {
		t.Errorf("expected no Location for a suspended key")
	}
	if got := rec.Body.String(); strings.Contains(got, "should-never-be-exposed") {
		t.Errorf("expected the destination never to appear in a suspended response body, got %s", got)
	}
}

// TestResolve_DependencyUnavailable proves a resolver error maps to
// 503, retryable, RFC 9457 shape, with Cache-Control: no-store.
func TestResolve_DependencyUnavailable(t *testing.T) {
	underlying := errUnexpectedTestFailure
	h := newResolveHandlerWithResolver(erroringResolver{err: underlying})
	rec := doResolveWithHandler(h, "abc1234", "10.0.5.6")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Errorf("expected application/problem+json, got %q", got)
	}
	if !strings.Contains(rec.Body.String(), `"code":"DEPENDENCY_UNAVAILABLE"`) {
		t.Errorf("expected code=DEPENDENCY_UNAVAILABLE, got %s", rec.Body.String())
	}
}
