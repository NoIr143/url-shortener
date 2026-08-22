package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// Creator is the minimal surface the Creation API deployment unit
// (ARC-002) needs. It intentionally does not include Get — the Creation
// API and Redirect Resolver are separate deployment units per ADR-001,
// and a Creation API instance has no business path that reads back a
// mapping by key.
type Creator interface {
	Create(ctx context.Context, shortKey, destination string) (created bool, resolvedKey string, err error)
}

// Resolver is the minimal surface the Redirect Resolver deployment unit
// (ARC-003) needs. It intentionally does not include Create — satisfied
// by *internal/cache.RedirectCache in production (which itself has no
// Create method), keeping the redirect path unable to accidentally
// create a mapping.
type Resolver interface {
	Get(ctx context.Context, shortKey string) (destination string, ok bool, err error)
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

func writeProblem(w http.ResponseWriter, status int, code, title, detail string, retryable bool) {
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

// CreateHandler implements POST /api/v1/urls (ARC-002), per
// docs/ACCEPTANCE_CRITERIA.md Part A.
type CreateHandler struct {
	store   Creator
	keyGen  func() string
	limiter *IPRateLimiter
}

func NewCreateHandler(store Creator, keyGen func() string, limiter *IPRateLimiter) *CreateHandler {
	return &CreateHandler{store: store, keyGen: keyGen, limiter: limiter}
}

type createRequest struct {
	Destination string `json:"destination"`
}

type createResponse struct {
	ShortKey string `json:"shortKey"`
	ShortURL string `json:"shortUrl"`
}

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

	if err := ValidateDestination(req.Destination); err != nil {
		writeProblem(w, http.StatusBadRequest, "INVALID_DESTINATION", "Destination is not acceptable", err.Error(), false)
		return
	}

	key := h.keyGen()
	created, resolvedKey, err := h.store.Create(r.Context(), key, req.Destination)
	if err != nil {
		writeProblem(w, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "Temporary failure", "", true)
		return
	}

	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(createResponse{ShortKey: resolvedKey, ShortURL: "https://short.example/" + resolvedKey})
}

// ResolveHandler implements GET /{shortKey} (ARC-003), per
// docs/ACCEPTANCE_CRITERIA.md Part B.1.
type ResolveHandler struct {
	store   Resolver
	limiter *IPRateLimiter
}

func NewResolveHandler(store Resolver, limiter *IPRateLimiter) *ResolveHandler {
	return &ResolveHandler{store: store, limiter: limiter}
}

// Resolve serves a redirect. The destination is placed in Location only
// after passing ValidateDestination at creation time — this handler
// additionally strips any control character as defense in depth
// (NFR-SEC-003) even though none should ever be stored.
func (h *ResolveHandler) Resolve(w http.ResponseWriter, r *http.Request, shortKey string) {
	if !h.limiter.Allow(clientIP(r)) {
		writeProblem(w, http.StatusTooManyRequests, "RATE_LIMITED", "Too many requests", "resolution rate limit exceeded", true)
		return
	}

	if err := ValidateShortKey(shortKey); err != nil {
		writeProblem(w, http.StatusBadRequest, "INVALID_KEY", "That key is not valid", "", false)
		return
	}

	dest, ok, err := h.store.Get(r.Context(), shortKey)
	if err != nil {
		writeProblem(w, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "Temporary failure", "", true)
		return
	}
	if !ok {
		writeProblem(w, http.StatusNotFound, "UNKNOWN_KEY", "That link isn't valid", "", false)
		return
	}

	for _, r := range dest {
		if r < 0x20 || r == 0x7f {
			// Should be unreachable given creation-time validation; fail
			// closed rather than ever emitting a header-injection payload.
			writeProblem(w, http.StatusInternalServerError, "CORRUPT_DESTINATION", "Stored destination failed a safety check", "", false)
			return
		}
	}

	w.Header().Set("Location", dest)
	w.WriteHeader(http.StatusFound) // 302, docs/decisions/DEC-002.md
}
