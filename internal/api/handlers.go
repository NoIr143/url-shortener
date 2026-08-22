package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"url-shortener/internal/application"
	"url-shortener/internal/domain"
)

// Creator is the minimal surface the Creation API deployment unit
// (ARC-002) needs — satisfied by *internal/application.CreationUseCase
// (T6-03) in production. It intentionally does not include Get — the
// Creation API and Redirect Resolver are separate deployment units per
// ADR-001, and a Creation API instance has no business path that reads
// back a mapping by key.
type Creator interface {
	Create(ctx context.Context, destination domain.Destination) (application.Result, error)
}

// Resolver is the minimal surface the Redirect Resolver deployment unit
// (ARC-003) needs — satisfied by *internal/application.ResolveUseCase
// (T8-02) in production. It intentionally does not include Create —
// keeping the redirect path unable to accidentally create a mapping.
type Resolver interface {
	Resolve(ctx context.Context, key domain.ShortKey) (application.ResolveResult, error)
}

// problemDetail is the RFC 9457 shape confirmed at docs/decisions/DEC-009.md.
type problemDetail struct {
	Type      string `json:"type,omitempty"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Detail    string `json:"detail,omitempty"`
	Code      string `json:"code"`
	Retryable bool   `json:"retryable"`
}

// setSecurityHeaders applies the response headers common to every
// response this service sends (T8-03; NFR-SEC-003):
//   - Cache-Control: no-store — no response here is safe for an
//     intermediary to cache past this request. This matters even for
//     errors: a cached 410 (Suspended) would survive a later
//     reinstatement just as wrongly as a cached 301 would have survived
//     a suspension (docs/decisions/DEC-002.md's entire rationale for
//     choosing 302 over 301), and a cached 404 would survive a later
//     creation of that same key.
//   - X-Content-Type-Options: nosniff — a response body is never meant
//     to be interpreted as anything other than its declared
//     Content-Type; directly addresses NFR-SEC-003's "script execution
//     in shipped error surfaces" concern.
func setSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

func writeProblem(w http.ResponseWriter, status int, code, title, detail string, retryable bool) {
	setSecurityHeaders(w)
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problemDetail{
		Title: title, Status: status, Detail: detail, Code: code, Retryable: retryable,
	})
}

func clientIP(r *http.Request) string {
	// Boundary-test helper: real deployments read this from the edge
	// (ARC-001), not directly from RemoteAddr; tests only need a stable
	// per-caller key for the rate-limit mechanism under test.
	if idx := strings.LastIndex(r.RemoteAddr, ":"); idx >= 0 {
		return r.RemoteAddr[:idx]
	}
	return r.RemoteAddr
}

// CreateHandler implements POST /api/v1/urls (ARC-002; T6-04), per
// docs/ACCEPTANCE_CRITERIA.md Part A and docs/decisions/DEC-009.md's
// route/schema/error contract.
type CreateHandler struct {
	useCase Creator
	limiter *IPRateLimiter
}

func NewCreateHandler(useCase Creator, limiter *IPRateLimiter) *CreateHandler {
	return &CreateHandler{useCase: useCase, limiter: limiter}
}

type createRequest struct {
	Destination string `json:"destination"`
}

type createResponse struct {
	ShortKey string `json:"shortKey"`
	ShortURL string `json:"shortUrl"`
}

// Create validates the request into a domain.Destination (T6-02) and
// delegates commit behavior entirely to the Creator (T6-03's
// CreationUseCase in production) — this handler's own job is only
// request/response translation and RFC 9457 error mapping (DEC-009),
// not any of FR-004 to FR-008's actual commit logic.
func (h *CreateHandler) Create(w http.ResponseWriter, r *http.Request) {
	if !h.limiter.Allow(clientIP(r)) {
		writeProblem(w, http.StatusTooManyRequests, "RATE_LIMITED", "Too many requests", "creation rate limit exceeded", true)
		return
	}

	var req createRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, http.StatusBadRequest, "MALFORMED_REQUEST", "Malformed request body", err.Error(), false)
		return
	}

	destination, err := domain.NewDestination(req.Destination)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "INVALID_DESTINATION", "Destination is not acceptable", err.Error(), false)
		return
	}

	result, err := h.useCase.Create(r.Context(), destination)
	if err != nil {
		// Both outcomes are safe to retry with the identical request
		// (docs/decisions/DEC-009.md's timeout/retry contract) and both
		// leave no resolvable key (FR-008) — they differ only in the
		// stable `code` field so a client/operator can tell a
		// commit-time conflict (rare — internal/mapping.ErrDigestCollision,
		// effectively a SHA-256 collision) apart from a generic
		// dependency failure, without it changing retry behavior.
		if errors.Is(err, application.ErrConflict) {
			writeProblem(w, http.StatusServiceUnavailable, "COMMIT_CONFLICT", "Could not safely commit", "", true)
			return
		}
		writeProblem(w, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "Temporary failure", "", true)
		return
	}

	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(createResponse{ShortKey: result.ShortKey.String(), ShortURL: "https://short.example/" + result.ShortKey.String()})
}

// ResolveHandler implements GET /{shortKey} (ARC-003; T8-03), per
// docs/ACCEPTANCE_CRITERIA.md Part B.1.
type ResolveHandler struct {
	useCase Resolver
	limiter *IPRateLimiter
}

func NewResolveHandler(useCase Resolver, limiter *IPRateLimiter) *ResolveHandler {
	return &ResolveHandler{useCase: useCase, limiter: limiter}
}

// Resolve validates the path segment into a domain.ShortKey (T8-01)
// and delegates entirely to the Resolver (T8-02's ResolveUseCase in
// production) — this handler's own job is only request/response
// translation and status mapping, not any of FR-009 to FR-016's actual
// resolution logic.
//
// No manual control-character check on the destination is needed here
// (unlike the pre-T8-02 version of this handler): domain.Destination
// cannot be constructed with one — that's enforced at the point
// ResolveUseCase's adapter reads the stored record, structurally, not
// by a runtime check that has to remember to run.
func (h *ResolveHandler) Resolve(w http.ResponseWriter, r *http.Request, shortKey string) {
	if !h.limiter.Allow(clientIP(r)) {
		writeProblem(w, http.StatusTooManyRequests, "RATE_LIMITED", "Too many requests", "resolution rate limit exceeded", true)
		return
	}

	key, err := domain.NewShortKey(shortKey)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "INVALID_KEY", "That key is not valid", "", false)
		return
	}

	result, err := h.useCase.Resolve(r.Context(), key)
	if err != nil {
		writeProblem(w, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "Temporary failure", "", true)
		return
	}

	switch result.Status {
	case application.ResolveStatusActive:
		setSecurityHeaders(w)
		w.Header().Set("Location", result.Destination.String())
		w.WriteHeader(http.StatusFound) // 302, docs/decisions/DEC-002.md
	case application.ResolveStatusSuspended:
		// FR-012: 410, no Location, no destination in the body —
		// ResolveResult structurally has no Destination for this
		// outcome (T8-02), so there is nothing here that could leak it.
		writeProblem(w, http.StatusGone, "SUSPENDED_KEY", "That link is no longer active", "", false)
	case application.ResolveStatusUnknown:
		writeProblem(w, http.StatusNotFound, "UNKNOWN_KEY", "That link isn't valid", "", false)
	default:
		// Unreachable in practice: ResolveUseCase's own switch already
		// fails closed (returns an error, handled above) for anything
		// that isn't one of the three ResolveStatus constants — this
		// default exists so a future new ResolveStatus value can't
		// silently fall through to a redirect if this handler is ever
		// not updated to match it (NFR-SAFE-001: fail closed, not open).
		writeProblem(w, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "Temporary failure", "", true)
	}
}
