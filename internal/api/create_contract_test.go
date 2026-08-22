// T6-04 contract tests: docs/decisions/DEC-009.md's full response-class
// set for POST /api/v1/urls — 201/200 (success/repeat), 503 in its two
// distinguishable flavors (dependency-unavailable vs. commit-conflict),
// RFC 9457 shape. The pre-existing attack corpus (attack_corpus_test.go)
// already covers 400/429; none of it previously exercised 200 or 503 at
// the handler level, a real gap this file closes.
package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"url-shortener/internal/application"
	"url-shortener/internal/domain"
)

// errUnexpectedTestFailure stands in for "any other" use-case error —
// deliberately not application.ErrConflict, so
// TestCreate_DependencyUnavailable and TestCreate_CommitConflict
// exercise the two different branches of Create's error mapping.
var errUnexpectedTestFailure = errors.New("simulated dependency failure")

// erroringCreator lets a test script exactly one Create outcome —
// standing in for internal/application.CreationUseCase reporting a
// failure, without needing real infrastructure.
type erroringCreator struct {
	err error
}

func (e erroringCreator) Create(ctx context.Context, destination domain.Destination) (application.Result, error) {
	return application.Result{}, e.err
}

func newCreateHandlerWithCreator(c Creator) *CreateHandler {
	return NewCreateHandler(c, NewIPRateLimiter(10, time.Minute))
}

func doCreateWithHandler(h *CreateHandler, body, remoteAddr string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/urls", strings.NewReader(body))
	req.RemoteAddr = remoteAddr + ":12345"
	rec := httptest.NewRecorder()
	h.Create(rec, req)
	return rec
}

// TestCreate_ExactRepeatReturns200 proves DEC-009's response schema:
// 201 for a new mapping, 200 for a byte-identical repeat (FR-004),
// same shortKey both times.
func TestCreate_ExactRepeatReturns200(t *testing.T) {
	h := newTestHandlers()
	body := `{"destination":"https://example.com/repeat-contract-test"}`

	first := doCreate(h, body, "10.0.4.1")
	if first.Code != http.StatusCreated {
		t.Fatalf("first request: expected 201, got %d: %s", first.Code, first.Body.String())
	}

	second := doCreate(h, body, "10.0.4.2")
	if second.Code != http.StatusOK {
		t.Fatalf("second (exact-repeat) request: expected 200, got %d: %s", second.Code, second.Body.String())
	}
	if first.Body.String() != second.Body.String() {
		t.Fatalf("expected the repeat to return the identical body: first=%s second=%s", first.Body.String(), second.Body.String())
	}
}

// TestCreate_DependencyUnavailable proves a generic use-case failure
// maps to 503 DEPENDENCY_UNAVAILABLE, retryable, RFC 9457 shape, and no
// key is ever present in the response body (FR-008 enforced at the
// transport boundary too, not just inside the use case).
func TestCreate_DependencyUnavailable(t *testing.T) {
	h := newCreateHandlerWithCreator(erroringCreator{err: errUnexpectedTestFailure})
	rec := doCreateWithHandler(h, `{"destination":"https://example.com/unavailable"}`, "10.0.4.3")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Errorf("expected application/problem+json, got %q", got)
	}
	if !strings.Contains(rec.Body.String(), `"code":"DEPENDENCY_UNAVAILABLE"`) {
		t.Errorf("expected code=DEPENDENCY_UNAVAILABLE, got %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"retryable":true`) {
		t.Errorf("expected retryable=true, got %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"shortKey"`) {
		t.Errorf("expected no shortKey field in an error response, got %s", rec.Body.String())
	}
}

// TestCreate_CommitConflict proves application.ErrConflict maps to a
// distinct `code` (COMMIT_CONFLICT) from a generic dependency failure —
// same status/retryable contract (still safe to retry, per DEC-009),
// different observability, so an operator can tell the rare
// digest-collision fail-closed case apart from ordinary unavailability.
func TestCreate_CommitConflict(t *testing.T) {
	h := newCreateHandlerWithCreator(erroringCreator{err: application.ErrConflict})
	rec := doCreateWithHandler(h, `{"destination":"https://example.com/conflict"}`, "10.0.4.4")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"code":"COMMIT_CONFLICT"`) {
		t.Errorf("expected code=COMMIT_CONFLICT, got %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"retryable":true`) {
		t.Errorf("expected retryable=true, got %s", rec.Body.String())
	}
}
